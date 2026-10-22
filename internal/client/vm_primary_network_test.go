package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// scriptedAnswer is one answer of a scriptedServer: a status and a body, or a connection that is
// closed without an answer.
type scriptedAnswer struct {
	status int
	body   string
	drop   bool
}

// scriptedServer answers the requests it is sent with the answers it was given, in order, and
// keeps answering with the last one when they run out.
func scriptedServer(t *testing.T, answers ...scriptedAnswer) (*httptest.Server, func() []recordedRequest) {
	t.Helper()
	var (
		mu   sync.Mutex
		seen []recordedRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, recordedRequest{r.Method, r.URL.Path, r.URL.RawQuery, string(b)})
		n := len(seen)
		mu.Unlock()
		answer := answers[len(answers)-1]
		if n <= len(answers) {
			answer = answers[n-1]
		}
		if answer.drop {
			panic(http.ErrAbortHandler)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(answer.status)
		_, _ = w.Write([]byte(answer.body))
	}))
	t.Cleanup(server.Close)
	return server, func() []recordedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedRequest(nil), seen...)
	}
}

const (
	movedBody = `{"success":true,"data":{"id":9071,"networkId":412,"networkName":"backend","ip":"192.168.20.15",
		"mac":"02:AA:BB:CC:DD:EE","gateway":"192.168.20.1","previousNetworkId":411,"changed":true,
		"gatewayMoved":true,"requiresGuestConfiguration":false,"pendingRecovery":false},"errors":null}`
	forecastBody = `{"success":true,"data":{"id":null,"networkId":412,"networkName":"backend","ip":"192.168.20.2",
		"mac":null,"gateway":"192.168.20.1","previousNetworkId":411,"changed":true,"gatewayMoved":true,
		"requiresGuestConfiguration":true,"pendingRecovery":false},"errors":null}`
)

func refusal(status, code int) scriptedAnswer {
	return scriptedAnswer{status: status, body: `{"success":false,"data":null,"errors":[{"code":` + strconv.Itoa(code) + `,"message":"refused"}]}`}
}

// quickly asks again at once and records the attempts it was asked after.
func quickly(asked *[]int) MoveOption {
	return WithMovePause(func(attempt int) time.Duration {
		*asked = append(*asked, attempt)
		return time.Millisecond
	})
}

func TestMovePrimaryLocalNetwork(t *testing.T) {
	server, seen := scriptedServer(t, scriptedAnswer{status: 200, body: movedBody})
	c := newTestClient(t, server)

	moved, err := c.MovePrimaryLocalNetwork(context.Background(), 118,
		MovePrimaryLocalNetworkRequest{NetworkID: 412, IP: "192.168.20.15"}, &RequestOpts{Region: "UZ5", ProjectTag: "proj"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := seen()
	if len(got) != 1 {
		t.Fatalf("requests = %+v, want one", got)
	}
	if got[0].method != http.MethodPut || got[0].path != "/panel-main/api/v2/vms/118/primary-local-network" || got[0].query != "projectTag=proj&region=UZ5" {
		t.Errorf("request = %+v, want a PUT of /panel-main/api/v2/vms/118/primary-local-network?projectTag=proj&region=UZ5", got[0])
	}
	if want := `{"networkId":412,"ip":"192.168.20.15"}`; got[0].body != want {
		t.Errorf("body = %s, want %s", got[0].body, want)
	}
	if moved.ID == nil || *moved.ID != 9071 || moved.NetworkID != 412 || moved.NetworkName == nil || *moved.NetworkName != "backend" ||
		moved.IP == nil || *moved.IP != "192.168.20.15" || moved.MAC == nil || *moved.MAC != "02:AA:BB:CC:DD:EE" ||
		moved.Gateway == nil || *moved.Gateway != "192.168.20.1" || moved.PreviousNetworkID == nil || *moved.PreviousNetworkID != 411 {
		t.Errorf("interface not decoded: %+v", moved)
	}
	if !moved.Changed || !moved.GatewayMoved || moved.RequiresGuestConfiguration || moved.PendingRecovery {
		t.Errorf("changed=%v gatewayMoved=%v requiresGuestConfiguration=%v pendingRecovery=%v, want true true false false",
			moved.Changed, moved.GatewayMoved, moved.RequiresGuestConfiguration, moved.PendingRecovery)
	}
}

func TestMovePrimaryLocalNetwork_WithoutAnAddressTheBodyHasNone(t *testing.T) {
	server, seen := scriptedServer(t, scriptedAnswer{status: 200, body: movedBody})
	c := newTestClient(t, server)

	if _, err := c.MovePrimaryLocalNetwork(context.Background(), 118, MovePrimaryLocalNetworkRequest{NetworkID: 412}, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := seen()[0]
	if want := `{"networkId":412}`; got.body != want {
		t.Errorf("body = %s, want %s: an empty ip must not be sent, the panel would read it as an address", got.body, want)
	}
	if got.query != "" {
		t.Errorf("query = %q, want none: the client's own region and project travel in the headers", got.query)
	}
}

func TestMovePrimaryLocalNetwork_DryRunIsAQueryParameter(t *testing.T) {
	server, seen := scriptedServer(t, scriptedAnswer{status: 200, body: forecastBody})
	c := newTestClient(t, server)

	forecast, err := c.MovePrimaryLocalNetwork(context.Background(), 118,
		MovePrimaryLocalNetworkRequest{NetworkID: 412, DryRun: true}, &RequestOpts{Region: "UZ5", ProjectTag: "proj"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := seen()[0]
	if got.method != http.MethodPut || got.query != "projectTag=proj&region=UZ5&dryRun=true" {
		t.Errorf("request = %+v, want a PUT with the scope and dryRun=true in the query", got)
	}
	if want := `{"networkId":412}`; got.body != want {
		t.Errorf("body = %s, want %s: dryRun is not a field of the body", got.body, want)
	}
	if forecast.ID != nil || forecast.MAC != nil {
		t.Errorf("a forecast has no interface yet: id=%v mac=%v", forecast.ID, forecast.MAC)
	}
	if forecast.IP == nil || *forecast.IP != "192.168.20.2" || !forecast.Changed || !forecast.RequiresGuestConfiguration {
		t.Errorf("forecast not decoded: %+v", forecast)
	}

	server2, seen2 := scriptedServer(t, scriptedAnswer{status: 200, body: forecastBody})
	c2 := newTestClient(t, server2)
	if _, err := c2.MovePrimaryLocalNetwork(context.Background(), 118, MovePrimaryLocalNetworkRequest{NetworkID: 412, DryRun: true}, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := seen2()[0].query; got != "dryRun=true" {
		t.Errorf("query = %q, want dryRun=true alone when there is no scope", got)
	}
}

func TestMovePrimaryLocalNetwork_ADryRunIsAskedOnce(t *testing.T) {
	// A check that fails says nothing is to be waited for: the caller sees the answer and leaves
	// the VM alone. Only a 503 is worth another try, as for every call of the client.
	for _, tc := range []struct {
		name   string
		answer scriptedAnswer
	}{
		{"a refusal", refusal(409, PanelCodeGatewayNotMovable)},
		{"the panel's catch-all", refusal(500, PanelCodePlatformError)},
		{"a bad gateway", scriptedAnswer{status: 502, body: "<html>bad gateway</html>"}},
		{"a connection that is closed", scriptedAnswer{drop: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, seen := scriptedServer(t, tc.answer)
			c := newTestClient(t, server)

			// A check that was asked again would go on for minutes: a deadline makes that a failure of this test.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, err := c.MovePrimaryLocalNetwork(ctx, 118, MovePrimaryLocalNetworkRequest{NetworkID: 412, DryRun: true}, nil)
			if err == nil {
				t.Fatal("want an error")
			}
			if IsMoveOutcomeUnknown(err) {
				t.Errorf("err = %v: a check does not wait for an outcome, there is none", err)
			}
			if n := len(seen()); n != 1 {
				t.Errorf("%d requests, want 1", n)
			}
		})
	}
}

func TestMovePrimaryLocalNetwork_ADefiniteAnswerIsReturnedAtOnce(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer scriptedAnswer
		code   int
	}{
		{"no such VM", refusal(404, PanelCodeVMNotFound), PanelCodeVMNotFound},
		{"no such network", refusal(404, 703), 703},
		{"the VM is in a group", refusal(403, PanelCodeVMInAutoScalingGroup), PanelCodeVMInAutoScalingGroup},
		{"the role of the VM", refusal(403, PanelCodeBadVMRole), PanelCodeBadVMRole},
		{"already in the network", refusal(409, PanelCodeAlreadyInNetwork), PanelCodeAlreadyInNetwork},
		{"the address is taken", refusal(409, PanelCodeAddressTaken), PanelCodeAddressTaken},
		{"a transitional status", refusal(409, PanelCodeBadVMStatus), PanelCodeBadVMStatus},
		{"the VM is not stopped", refusal(409, PanelCodeVMNotStopped), PanelCodeVMNotStopped},
		{"a load balancer backend", refusal(409, PanelCodeLoadBalancerBackend), PanelCodeLoadBalancerBackend},
		{"the gateway cannot move", refusal(409, PanelCodeGatewayNotMovable), PanelCodeGatewayNotMovable},
		{"the main interface is unclear", refusal(409, PanelCodePrimaryUnclear), PanelCodePrimaryUnclear},
		{"the same network", refusal(409, PanelCodeSameNetworkAddress), PanelCodeSameNetworkAddress},
		{"the security groups", refusal(409, PanelCodeSecurityGroups), PanelCodeSecurityGroups},
		{"stuck, the operator is needed", refusal(409, PanelCodeMoveNeedsOperator), PanelCodeMoveNeedsOperator},
		{"a request the panel cannot read", refusal(400, 400), 400},
		{"the catch-all of the panel", refusal(500, PanelCodePlatformError), PanelCodePlatformError},
		{"the catch-all in another status", refusal(502, PanelCodePlatformError), PanelCodePlatformError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, seen := scriptedServer(t, tc.answer)
			c := newTestClient(t, server)
			var asked []int

			moved, err := c.MovePrimaryLocalNetwork(context.Background(), 118, MovePrimaryLocalNetworkRequest{NetworkID: 412}, nil, quickly(&asked))
			if !IsAPIError(err, tc.code) {
				t.Fatalf("err = %v, want API error %d", err, tc.code)
			}
			if moved != nil {
				t.Errorf("moved = %+v with an error", moved)
			}
			if IsMoveOutcomeUnknown(err) {
				t.Errorf("err = %v: a definite answer is not an unknown outcome", err)
			}
			if n := len(seen()); n != 1 || len(asked) != 0 {
				t.Errorf("%d requests, %d pauses, want 1 and none: a definite answer is not asked again", n, len(asked))
			}
		})
	}
}

func TestMovePrimaryLocalNetwork_AnAnswerThatDoesNotSayIsAskedAgain(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first scriptedAnswer
	}{
		{"a connection that is closed", scriptedAnswer{drop: true}},
		{"a bad gateway", scriptedAnswer{status: 502, body: "<html>bad gateway</html>"}},
		{"a gateway timeout", scriptedAnswer{status: 504, body: ""}},
		{"a 500 without the code of the catch-all", refusal(500, 500)},
		{"a 500 without an envelope", scriptedAnswer{status: 500, body: "boom"}},
		{"a 503 that is not the busy one", refusal(503, 744)},
		{"a 503 of whatever stands before the panel", scriptedAnswer{status: 503, body: "unavailable"}},
		{"the VM is busy", refusal(503, PanelCodeVMBusy)},
		{"a 200 that cannot be read", scriptedAnswer{status: 200, body: "<html>ok</html>"}},
		{"a 200 without data", scriptedAnswer{status: 200, body: `{"success":true,"data":null}`}},
		{"a redirect", scriptedAnswer{status: 302, body: ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, seen := scriptedServer(t, tc.first, scriptedAnswer{status: 200, body: movedBody})
			c := newTestClient(t, server)
			var asked []int

			moved, err := c.MovePrimaryLocalNetwork(context.Background(), 118, MovePrimaryLocalNetworkRequest{NetworkID: 412, IP: "192.168.20.15"}, nil, quickly(&asked))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if moved.NetworkID != 412 || !moved.Changed {
				t.Errorf("moved = %+v, want the answer of the second request", moved)
			}
			got := seen()
			if len(got) != 2 || len(asked) != 1 || asked[0] != 0 {
				t.Fatalf("%d requests, pauses after attempts %v, want 2 requests and one pause after attempt 0", len(got), asked)
			}
			if got[0].body != got[1].body || got[0].path != got[1].path || got[0].method != got[1].method {
				t.Errorf("the repeated request is not the same: %+v then %+v", got[0], got[1])
			}
		})
	}
}

func TestMovePrimaryLocalNetwork_ThePauseGrowsWithTheAttempts(t *testing.T) {
	server, seen := scriptedServer(t,
		scriptedAnswer{drop: true}, scriptedAnswer{status: 502}, refusal(503, PanelCodeVMBusy), scriptedAnswer{status: 504},
		scriptedAnswer{status: 200, body: movedBody})
	c := newTestClient(t, server)
	var asked []int

	if _, err := c.MovePrimaryLocalNetwork(context.Background(), 118, MovePrimaryLocalNetworkRequest{NetworkID: 412}, nil, quickly(&asked)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n := len(seen()); n != 5 {
		t.Errorf("%d requests, want 5", n)
	}
	if len(asked) != 4 || asked[0] != 0 || asked[1] != 1 || asked[2] != 2 || asked[3] != 3 {
		t.Errorf("pauses were asked after attempts %v, want 0 1 2 3", asked)
	}
}

func TestMovePrimaryLocalNetwork_AnUnknownOutcomeEndsWithTheTime(t *testing.T) {
	server, seen := scriptedServer(t, scriptedAnswer{status: 502, body: "<html>bad gateway</html>"})
	c := newTestClient(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	moved, err := c.MovePrimaryLocalNetwork(ctx, 118, MovePrimaryLocalNetworkRequest{NetworkID: 412}, nil,
		WithMovePause(func(int) time.Duration { return 20 * time.Millisecond }))

	var unknown *MoveOutcomeUnknownError
	if !errors.As(err, &unknown) {
		t.Fatalf("err = %v, want a MoveOutcomeUnknownError", err)
	}
	if moved != nil {
		t.Errorf("moved = %+v with an error", moved)
	}
	if unknown.Attempts < 2 || len(seen()) < unknown.Attempts-1 {
		t.Errorf("attempts = %d for %d requests, want more than one, and the requests sent", unknown.Attempts, len(seen()))
	}
	var apiErr *APIError
	if errors.As(unknown.Last, &apiErr) && apiErr.StatusCode != 502 {
		t.Errorf("last = %v, want the 502 that was the last answer", unknown.Last)
	}
	if IsAPIError(err, PanelCodePlatformError) {
		t.Errorf("err = %v: it is an unknown outcome, and not an answer of the panel", err)
	}
}

func TestMovePrimaryLocalNetwork_AHungRequestEndsAsAnUnknownOutcome(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)
	c := newTestClient(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := c.MovePrimaryLocalNetwork(ctx, 118, MovePrimaryLocalNetworkRequest{NetworkID: 412}, nil, WithMovePause(func(int) time.Duration { return time.Millisecond }))

	var unknown *MoveOutcomeUnknownError
	if !errors.As(err, &unknown) || unknown.Attempts != 1 {
		t.Fatalf("err = %v, want a MoveOutcomeUnknownError after one attempt", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the deadline of the context in the chain", err)
	}
}

func TestMovePrimaryLocalNetwork_APauseThatDoesNotFitInTheTimeEndsIt(t *testing.T) {
	server, seen := scriptedServer(t, scriptedAnswer{status: 502})
	c := newTestClient(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	start := time.Now()
	_, err := c.MovePrimaryLocalNetwork(ctx, 118, MovePrimaryLocalNetworkRequest{NetworkID: 412}, nil,
		WithMovePause(func(int) time.Duration { return time.Hour }))

	if !IsMoveOutcomeUnknown(err) {
		t.Fatalf("err = %v, want an unknown outcome", err)
	}
	if n := len(seen()); n != 1 {
		t.Errorf("%d requests, want 1: waiting past the deadline would only hold the VM stopped", n)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("returned after %v, want at once: the pause was not waited for", time.Since(start))
	}
}

func TestMovePrimaryLocalNetwork_WithoutADeadlineTheTimeIsBounded(t *testing.T) {
	server, seen := scriptedServer(t, scriptedAnswer{status: 502})
	c := newTestClient(t, server)

	_, err := c.MovePrimaryLocalNetwork(context.Background(), 118, MovePrimaryLocalNetworkRequest{NetworkID: 412}, nil,
		WithMovePause(func(int) time.Duration { return moveUnknownOutcomeBudget + time.Second }))

	if !IsMoveOutcomeUnknown(err) {
		t.Fatalf("err = %v, want an unknown outcome: a context without a deadline must not be asked for ever", err)
	}
	if n := len(seen()); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
}

// A context without a deadline asks again for as long as the other calls of the provider wait for
// a VM that is busy: the same five minutes, not the two of a check. Waiting is tested above with
// a pause that is longer than any budget; this is the budget.
func TestMovePrimaryLocalNetwork_TheBudgetWithoutADeadlineIsTheLongWait(t *testing.T) {
	budget := moveUnknownOutcomeBudget
	if budget != RetryTimeoutLong {
		t.Errorf("moveUnknownOutcomeBudget = %v, want RetryTimeoutLong (%v)", budget, RetryTimeoutLong)
	}
}

func TestMovePrimaryLocalNetwork_ACancelledContextStopsTheWaiting(t *testing.T) {
	server, seen := scriptedServer(t, scriptedAnswer{status: 502})
	c := newTestClient(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pauses := make(chan struct{}, 1)

	done := make(chan error, 1)
	go func() {
		_, err := c.MovePrimaryLocalNetwork(ctx, 118, MovePrimaryLocalNetworkRequest{NetworkID: 412}, nil,
			WithMovePause(func(int) time.Duration {
				pauses <- struct{}{}
				return 30 * time.Second
			}))
		done <- err
	}()

	<-pauses
	cancel()
	select {
	case err := <-done:
		if !IsMoveOutcomeUnknown(err) {
			t.Errorf("err = %v, want an unknown outcome", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the call kept waiting after its context was cancelled")
	}
	if n := len(seen()); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
}

func TestMovePrimaryLocalNetwork_ADoneContextSendsNothingAgain(t *testing.T) {
	server, seen := scriptedServer(t, scriptedAnswer{status: 502})
	c := newTestClient(t, server)
	ctx, cancel := context.WithCancel(context.Background())
	var asked []int
	option := WithMovePause(func(attempt int) time.Duration {
		asked = append(asked, attempt)
		cancel()
		return time.Millisecond
	})

	_, err := c.MovePrimaryLocalNetwork(ctx, 118, MovePrimaryLocalNetworkRequest{NetworkID: 412}, nil, option)

	var unknown *MoveOutcomeUnknownError
	if !errors.As(err, &unknown) || unknown.Attempts != 1 {
		t.Fatalf("err = %v, want an unknown outcome after one attempt", err)
	}
	if n := len(seen()); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
}

func TestMovePrimaryLocalNetwork_BusyEndsAsAnUnknownOutcomeWithTheCodeInTheChain(t *testing.T) {
	server, seen := scriptedServer(t, refusal(503, PanelCodeVMBusy))
	c := newTestClient(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	_, err := c.MovePrimaryLocalNetwork(ctx, 118, MovePrimaryLocalNetworkRequest{NetworkID: 412}, nil,
		WithMovePause(func(attempt int) time.Duration {
			if attempt < 2 {
				return time.Millisecond
			}
			return time.Hour
		}))

	var unknown *MoveOutcomeUnknownError
	if !errors.As(err, &unknown) || unknown.Attempts != 3 || len(seen()) != 3 {
		t.Fatalf("err = %v after %d requests, want an unknown outcome after 3", err, len(seen()))
	}
	if !IsAPIError(err, PanelCodeVMBusy) {
		t.Errorf("err = %v, want one that still says the VM was busy (961)", err)
	}
}

func TestIsRouteMissing(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"the panel's answer for a path it does not serve", &APIError{StatusCode: 404, Codes: []int{404}}, true},
		{"the same with a message", &APIError{StatusCode: 404, Codes: []int{404}, Message: "No static resource"}, true},
		{"a method the static handler refuses", &APIError{StatusCode: 405, Codes: []int{405}}, true},
		{"a bare 404", &APIError{StatusCode: 404}, true},
		{"a bare 405", &APIError{StatusCode: 405}, true},
		{"no such VM", &APIError{StatusCode: 404, Codes: []int{PanelCodeVMNotFound}}, false},
		{"no such network", &APIError{StatusCode: 404, Codes: []int{703}}, false},
		{"a 404 that carries 404 beside another code", &APIError{StatusCode: 404, Codes: []int{PanelCodeVMNotFound, 404}}, true},
		{"code 404 in another status", &APIError{StatusCode: 409, Codes: []int{404}}, false},
		{"a refusal", &APIError{StatusCode: 409, Codes: []int{PanelCodeVMNotStopped}}, false},
		{"a bare 500", &APIError{StatusCode: 500}, false},
		{"a bare 400", &APIError{StatusCode: 400}, false},
		{"not an API error", errors.New("request failed"), false},
		{"nothing", nil, false},
		{"wrapped", wrapped(&APIError{StatusCode: 404, Codes: []int{404}}), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRouteMissing(tc.err); got != tc.want {
				t.Errorf("IsRouteMissing(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func wrapped(err error) error { return &wrapError{err} }

type wrapError struct{ error }

func (w *wrapError) Unwrap() error { return w.error }

func TestMoveAnswerIsDefinite(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"a 4xx with a code", &APIError{StatusCode: 409, Codes: []int{PanelCodeVMNotStopped}}, true},
		{"a bare 4xx", &APIError{StatusCode: 403}, true},
		{"a 404 of the route", &APIError{StatusCode: 404, Codes: []int{404}}, true},
		{"a 400", &APIError{StatusCode: 400, Codes: []int{400}}, true},
		{"the catch-all", &APIError{StatusCode: 500, Codes: []int{PanelCodePlatformError}}, true},
		{"the catch-all among others", &APIError{StatusCode: 500, Codes: []int{704, PanelCodePlatformError}}, true},
		{"busy", &APIError{StatusCode: 503, Codes: []int{PanelCodeVMBusy}}, false},
		{"a 500 with another code", &APIError{StatusCode: 500, Codes: []int{500}}, false},
		{"a bare 500", &APIError{StatusCode: 500}, false},
		{"a 502", &APIError{StatusCode: 502}, false},
		{"a 503", &APIError{StatusCode: 503, Codes: []int{744}}, false},
		{"a 504", &APIError{StatusCode: 504}, false},
		{"a failure inside a 200", &APIError{StatusCode: 200}, false},
		{"a failure with codes inside a 200", &APIError{StatusCode: 200, Codes: []int{955}}, false},
		{"a redirect", &APIError{StatusCode: 302}, false},
		{"a request that failed", errors.New("request failed: EOF"), false},
		{"a reply that cannot be read", errors.New("unexpected response format (HTTP 200 OK)"), false},
		{"the context", context.DeadlineExceeded, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := moveAnswerIsDefinite(tc.err); got != tc.want {
				t.Errorf("moveAnswerIsDefinite(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestDefaultMovePause(t *testing.T) {
	// 2s, 4s, 8s, then 15s from the fourth attempt on, each with ±25% of jitter.
	for attempt, base := range map[int]time.Duration{0: 2 * time.Second, 1: 4 * time.Second, 2: 8 * time.Second, 3: 15 * time.Second, 4: 15 * time.Second, 7: 15 * time.Second, 8: 15 * time.Second, 63: 15 * time.Second, 1000: 15 * time.Second} {
		for i := 0; i < 200; i++ {
			got := defaultMovePause(attempt)
			if low, high := base-base/4, base+base/4; got < low || got > high {
				t.Fatalf("pause after attempt %d = %v, want within [%v, %v]", attempt, got, low, high)
			}
		}
	}
}

func TestWithQueryParam(t *testing.T) {
	for path, want := range map[string]string{
		"/a/b":              "/a/b?dryRun=true",
		"/a/b?region=UZ5":   "/a/b?region=UZ5&dryRun=true",
		"/a/b?x=1&region=2": "/a/b?x=1&region=2&dryRun=true",
	} {
		if got := withQueryParam(path, "dryRun", "true"); got != want {
			t.Errorf("withQueryParam(%q) = %q, want %q", path, got, want)
		}
	}
	if got, want := withQueryParam("/a", "q", "a b&c"), "/a?q=a+b%26c"; got != want {
		t.Errorf("withQueryParam escaped to %q, want %q", got, want)
	}
}
