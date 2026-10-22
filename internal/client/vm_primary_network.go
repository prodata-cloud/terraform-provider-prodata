package client

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Codes of the panel's answers to PUT /api/v2/vms/{id}/primary-local-network that the provider
// tells apart. The same numbers are used by the attach and detach of a local network interface
// (633, 787, 955, 956, 959, 960, 961); 963-969 belong to the move of the main interface.
const (
	// PanelCodePlatformError is the panel's catch-all (HTTP 500): the platform refused, or
	// something the panel does not map failed. For the move it also covers a call to the platform
	// whose outcome the panel does not know, so it never says that nothing was changed.
	PanelCodePlatformError = 627
	// PanelCodeVMNotFound: no such VM in this project (HTTP 404).
	PanelCodeVMNotFound = 633
	// PanelCodeVMInAutoScalingGroup: the VM is a member of an auto scaling group.
	PanelCodeVMInAutoScalingGroup = 787
	// PanelCodeAlreadyInNetwork: the VM already has an interface in the network.
	PanelCodeAlreadyInNetwork = 955
	// PanelCodeAddressTaken: the address is taken.
	PanelCodeAddressTaken = 956
	// PanelCodeBadVMStatus: the VM is in a transitional status.
	PanelCodeBadVMStatus = 959
	// PanelCodeBadVMRole: the VM is not an ordinary one (a router, a node of a cluster, ...).
	PanelCodeBadVMRole = 960
	// PanelCodeVMBusy: another operation holds the VM, or an earlier move of it was cut short
	// and the panel waits before it looks at what is left (HTTP 503).
	PanelCodeVMBusy = 961
	// PanelCodeVMNotStopped: the VM is not stopped.
	PanelCodeVMNotStopped = 963
	// PanelCodeLoadBalancerBackend: the VM is a backend of a load balancer in its network.
	PanelCodeLoadBalancerBackend = 964
	// PanelCodeGatewayNotMovable: the gateway of the VM cannot go to the new network.
	PanelCodeGatewayNotMovable = 965
	// PanelCodePrimaryUnclear: the main interface of the VM cannot be told from the others.
	PanelCodePrimaryUnclear = 966
	// PanelCodeSameNetworkAddress: changing the address inside the same network is not what the
	// move does.
	PanelCodeSameNetworkAddress = 967
	// PanelCodeSecurityGroups: the security groups of the main interface cannot be carried over.
	PanelCodeSecurityGroups = 968
	// PanelCodeMoveNeedsOperator: an earlier move of the VM is stuck and only the operator of the
	// platform can settle it.
	PanelCodeMoveNeedsOperator = 969
)

// moveUnknownOutcomeBudget is how long a move is asked again after answers that do not say
// whether it happened when the context carries no deadline of its own.
const moveUnknownOutcomeBudget = RetryTimeoutLong

// MovePrimaryLocalNetworkRequest is the body of PUT /api/v2/vms/{id}/primary-local-network. An
// empty IP asks the panel for the next free address of the network.
type MovePrimaryLocalNetworkRequest struct {
	NetworkID int64  `json:"networkId"`
	IP        string `json:"ip,omitempty"`

	// DryRun asks for the checks and the forecast only; the panel changes nothing. It is a query
	// parameter, not a field of the body.
	DryRun bool `json:"-"`
}

// MovedPrimaryLocalNetwork is the answer to a move: the main interface after it, and what was
// done. With DryRun it is a forecast: ID and MAC are nil (the interface does not exist yet) and IP
// is the address the move would take.
type MovedPrimaryLocalNetwork struct {
	ID          *int64  `json:"id"`
	NetworkID   int64   `json:"networkId"`
	NetworkName *string `json:"networkName"`
	IP          *string `json:"ip"`
	MAC         *string `json:"mac"`
	Gateway     *string `json:"gateway"`
	// PreviousNetworkID is the network the VM was in; nil when it was in NetworkID already.
	PreviousNetworkID *int64 `json:"previousNetworkId"`
	// Changed is false when the main interface was in the network already: nothing was changed
	// on the platform (the panel only puts its own record right when it was off).
	Changed bool `json:"changed"`
	// GatewayMoved is true when the VM has a default route and it goes through the new interface.
	GatewayMoved bool `json:"gatewayMoved"`
	// RequiresGuestConfiguration is true when the VM has no cloud-init, so nothing configures the
	// new address inside the guest.
	RequiresGuestConfiguration bool `json:"requiresGuestConfiguration"`
	// PendingRecovery is true when an earlier move of this VM, cut short by a failed request, was
	// settled (completed or taken back) before this one was looked at.
	PendingRecovery bool `json:"pendingRecovery"`
}

// MoveOutcomeUnknownError is what MovePrimaryLocalNetwork returns when it never got a definite
// answer from the panel within the time it had: the request failed, or the answers were the kind
// that does not say whether the move happened. The VM may be in either network, or in the middle
// of the move, which the panel settles on the next call.
type MoveOutcomeUnknownError struct {
	// Attempts is how many times the request was sent.
	Attempts int
	// Last is the last thing that went wrong: an answer of the panel, a failed request, or the
	// error of the context that ran out.
	Last error
}

func (e *MoveOutcomeUnknownError) Error() string {
	return fmt.Sprintf("no definite answer to the move of the main network after %d attempt(s); last: %v", e.Attempts, e.Last)
}

func (e *MoveOutcomeUnknownError) Unwrap() error { return e.Last }

// IsMoveOutcomeUnknown reports whether err is (or wraps) a MoveOutcomeUnknownError.
func IsMoveOutcomeUnknown(err error) bool {
	var unknown *MoveOutcomeUnknownError
	return errors.As(err, &unknown)
}

// IsRouteMissing reports whether err says the panel has no such route: a panel older than the
// move. The panel answers a path it does not serve with HTTP 404 (or 405 for a method the static
// resource handler refuses) and the status as the code of the envelope; a bare 404 or 405 comes
// from whatever stands before the panel. A 404 that carries another code is not this: 633 (no such
// VM) and the codes of a network that is not there are answers of the route itself.
func IsRouteMissing(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.StatusCode != http.StatusNotFound && apiErr.StatusCode != http.StatusMethodNotAllowed {
		return false
	}
	return len(apiErr.Codes) == 0 || apiErr.HasCode(apiErr.StatusCode)
}

// MoveOption tunes MovePrimaryLocalNetwork.
type MoveOption func(*moveCall)

type moveCall struct {
	pause func(attempt int) time.Duration
}

// WithMovePause sets how long the move waits before it asks again, after the attempt-th answer
// (counted from 0) that does not say whether the move happened. The default grows from two
// seconds to fifteen, with the jitter of RetryOnBusy.
func WithMovePause(pause func(attempt int) time.Duration) MoveOption {
	return func(call *moveCall) { call.pause = pause }
}

// MovePrimaryLocalNetwork moves the main interface of a VM to another local network, on the
// platform and in the panel's records. The VM has to be stopped; the panel refuses a running one
// (963).
//
// The request is idempotent: asked again after a move that went through it answers that nothing
// was changed (Changed is false), and asked again after one that was cut short it settles what
// is left of it first. So an answer that does not say whether the move happened (the connection
// dropped, an HTTP 5xx the panel did not map to a code, a reply that cannot be read) is followed
// by the same request after a growing pause, and so is 961, which says the panel is still busy
// with the VM. This goes on until the panel answers definitely or ctx ends; then the error is a
// MoveOutcomeUnknownError. A definite answer is returned as it is: a 4xx, and 627, which is
// definite but not a completion: the panel took nothing back, and the caller decides what to look
// at.
//
// With DryRun the panel only checks, so the call is repeated only while it answers 503.
func (c *Client) MovePrimaryLocalNetwork(ctx context.Context, vmID int64, req MovePrimaryLocalNetworkRequest, opts *RequestOpts, options ...MoveOption) (*MovedPrimaryLocalNetwork, error) {
	if req.DryRun {
		return RetryOnBusy(ctx, RetryTimeoutShort, func() (*MovedPrimaryLocalNetwork, error) {
			return c.movePrimaryLocalNetworkOnce(ctx, vmID, req, opts)
		})
	}

	call := moveCall{pause: defaultMovePause}
	for _, option := range options {
		option(&call)
	}
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		deadline = time.Now().Add(moveUnknownOutcomeBudget)
	}

	for attempt := 0; ; attempt++ {
		moved, err := c.movePrimaryLocalNetworkOnce(ctx, vmID, req, opts)
		if err == nil || moveAnswerIsDefinite(err) {
			return moved, err
		}
		pause := call.pause(attempt)
		if ctx.Err() != nil || time.Now().Add(pause).After(deadline) {
			return nil, &MoveOutcomeUnknownError{Attempts: attempt + 1, Last: err}
		}
		select {
		case <-ctx.Done():
			return nil, &MoveOutcomeUnknownError{Attempts: attempt + 1, Last: err}
		case <-time.After(pause):
		}
	}
}

func (c *Client) movePrimaryLocalNetworkOnce(ctx context.Context, vmID int64, req MovePrimaryLocalNetworkRequest, opts *RequestOpts) (*MovedPrimaryLocalNetwork, error) {
	path := scopedPath(fmt.Sprintf("/api/v2/vms/%d/primary-local-network", vmID), opts)
	if req.DryRun {
		path = withQueryParam(path, "dryRun", "true")
	}
	var moved MovedPrimaryLocalNetwork
	if err := c.Do(ctx, http.MethodPut, path, req, &moved, opts); err != nil {
		return nil, err
	}
	if moved.NetworkID == 0 {
		// A 200 without the answer the contract promises is not an answer: the panel may have
		// done the work, and the request is idempotent.
		return nil, errors.New("the panel answered 200 without data about the move")
	}
	return &moved, nil
}

// moveAnswerIsDefinite reports whether err is an answer after which the request is not sent
// again: the panel refused it (a 4xx, which comes before any work), or it answered with its
// catch-all 627. Anything else says nothing about whether the move happened: a request that
// failed, a reply that cannot be read, a 5xx without a code the panel gives to a definite
// outcome, and 961, which says to wait.
func moveAnswerIsDefinite(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch {
	case apiErr.HasCode(PanelCodeVMBusy):
		return false
	case apiErr.HasCode(PanelCodePlatformError):
		return true
	}
	return apiErr.StatusCode >= 400 && apiErr.StatusCode < 500
}

// defaultMovePause is the pause before the next attempt after the attempt-th one: two seconds,
// doubling to fifteen, with ±25% of jitter.
func defaultMovePause(attempt int) time.Duration {
	interval := retryMaxInterval
	if attempt < 8 {
		if doubled := retryInitialInterval << attempt; doubled < interval {
			interval = doubled
		}
	}
	jitter := time.Duration(rand.Int63n(int64(interval)/2)) - interval/4
	return interval + jitter
}

// withQueryParam adds one query parameter to a path that may already have some.
func withQueryParam(path, key, value string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + url.QueryEscape(key) + "=" + url.QueryEscape(value)
}
