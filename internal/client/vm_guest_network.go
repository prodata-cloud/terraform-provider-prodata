package client

import (
	"context"
	"fmt"
	"github.com/hashicorp/go-uuid"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const GuestNetworkContract = "guest-network-operation-v1"

type GuestNetworkState struct {
	Contract                 string `json:"contract"`
	RecoverableSubmission    *bool  `json:"recoverableSubmission"`
	RecoverablePowerChildren *bool  `json:"recoverablePowerChildren"`
	OperationUUID            string `json:"operationUuid"`
	Status                   string `json:"status"`
	Pending                  *bool  `json:"pending"`
	Verified                 *bool  `json:"verified"`
	Released                 *bool  `json:"released"`
	Managed                  *bool  `json:"managed"`
	CloudInitDisabled        *bool  `json:"cloudInitDisabled"`
	Supported                *bool  `json:"supported"`
	Configured               *bool  `json:"configured"`
	Driver                   string `json:"driver"`
	Reason                   string `json:"reason"`
}

type ConfigureGuestNetworkRequest struct {
	ExpectedMAC           string  `json:"expectedMac"`
	ExpectedIP            string  `json:"expectedIp"`
	ConfigureDefaultRoute bool    `json:"configureDefaultRoute"`
	Gateway               *string `json:"gateway,omitempty"`
	ExpectedGuestGateway  *string `json:"expectedGuestGateway,omitempty"`
}

func (s *GuestNetworkState) validate() error {
	if s.Supported == nil || s.Configured == nil || s.Driver != "netplan-v1" {
		return fmt.Errorf("the API did not confirm a supported guest network operation; upgrade the matching panel and core APIs")
	}
	if !*s.Supported && (*s.Configured || strings.TrimSpace(s.Reason) == "") {
		return fmt.Errorf("the API returned an inconsistent guest network result")
	}
	return nil
}

func (c *Client) GetVmGuestNetworkCapabilities(ctx context.Context, vmID int64, opts *RequestOpts) (*GuestNetworkState, error) {
	var result GuestNetworkState
	if err := c.Do(ctx, http.MethodGet, scopedPath(fmt.Sprintf("/api/v2/vms/%d/guest-network-capabilities", vmID), opts), nil, &result, opts); err != nil {
		return nil, err
	}
	if err := result.validate(); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) GetVmGuestNetworkState(ctx context.Context, vmID, interfaceID int64, req ConfigureGuestNetworkRequest, opts *RequestOpts) (*GuestNetworkState, error) {
	query := url.Values{"expectedMac": {req.ExpectedMAC}, "expectedIp": {req.ExpectedIP}, "configureDefaultRoute": {strconv.FormatBool(req.ConfigureDefaultRoute)}}
	if req.Gateway != nil {
		query.Set("gateway", *req.Gateway)
	}
	if req.ExpectedGuestGateway != nil {
		query.Set("expectedGuestGateway", *req.ExpectedGuestGateway)
	}
	if opts != nil {
		if opts.Region != "" {
			query.Set("region", opts.Region)
		}
		if opts.ProjectTag != "" {
			query.Set("projectTag", opts.ProjectTag)
		}
	}
	var result GuestNetworkState
	path := fmt.Sprintf("/api/v2/vms/%d/local-networks/%d/guest-configuration?%s", vmID, interfaceID, query.Encode())
	if err := c.Do(ctx, http.MethodGet, path, nil, &result, opts); err != nil {
		return nil, err
	}
	if err := result.validate(); err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *GuestNetworkState) RequireRecoverable() error {
	if s.Contract != GuestNetworkContract || s.RecoverableSubmission == nil || !*s.RecoverableSubmission {
		return fmt.Errorf("the panel must support recoverable guest operations before changing guest networking")
	}
	return nil
}

func (c *Client) DryRunVmGuestNetwork(ctx context.Context, vmID, interfaceID int64, req ConfigureGuestNetworkRequest, opts *RequestOpts) (*GuestNetworkState, error) {
	return c.guestNetworkMutation(ctx, http.MethodPost, "/dry-run", vmID, interfaceID, req, opts, false)
}
func (c *Client) ConfigureVmGuestNetwork(ctx context.Context, vmID, interfaceID int64, req ConfigureGuestNetworkRequest, opts *RequestOpts) (*GuestNetworkState, error) {
	return c.guestNetworkMutation(ctx, http.MethodPost, "", vmID, interfaceID, req, opts, true)
}
func (c *Client) ReleaseVmGuestNetwork(ctx context.Context, vmID, interfaceID int64, req ConfigureGuestNetworkRequest, opts *RequestOpts) (*GuestNetworkState, error) {
	return c.guestNetworkMutation(ctx, http.MethodDelete, "", vmID, interfaceID, req, opts, true)
}
func (c *Client) guestNetworkMutation(ctx context.Context, method, suffix string, vmID, interfaceID int64, req ConfigureGuestNetworkRequest, opts *RequestOpts, durable bool) (*GuestNetworkState, error) {
	var result GuestNetworkState
	endpoint := scopedPath(fmt.Sprintf("/api/v2/vms/%d/local-networks/%d/guest-configuration%s", vmID, interfaceID, suffix), opts)
	if err := c.Do(ctx, method, endpoint, req, &result, opts); err != nil {
		return nil, err
	}
	if err := result.validate(); err != nil {
		return nil, err
	}
	if err := result.RequireRecoverable(); err != nil {
		return nil, err
	}
	if durable && result.Pending != nil && *result.Pending {
		if _, err := uuid.ParseUUID(result.OperationUUID); err != nil {
			return nil, fmt.Errorf("pending guest operation has no valid durable UUID")
		}
		return &result, nil
	}
	if result.Verified == nil || !*result.Verified || !*result.Supported || durable && (result.CloudInitDisabled == nil || *result.CloudInitDisabled) {
		return nil, fmt.Errorf("guest network operation is unverified: %s", result.Reason)
	}
	if durable && (method == http.MethodDelete && (result.Released == nil || !*result.Released) || method != http.MethodDelete && !*result.Configured) {
		return nil, fmt.Errorf("guest network operation was not confirmed: %s", result.Reason)
	}
	return &result, nil
}

func (c *Client) ReserveVmGuestNetwork(ctx context.Context, vmID, interfaceID int64, req ConfigureGuestNetworkRequest, opts *RequestOpts) (*GuestNetworkState, error) {
	var result GuestNetworkState
	if err := c.Do(ctx, http.MethodPost, scopedPath(fmt.Sprintf("/api/v2/vms/%d/local-networks/%d/guest-configuration/reservations", vmID, interfaceID), opts), req, &result, opts); err != nil {
		return nil, err
	}
	if err := result.validate(); err != nil {
		return nil, err
	}
	if err := result.RequireRecoverable(); err != nil {
		return nil, err
	}
	if _, err := uuid.ParseUUID(result.OperationUUID); err != nil {
		return nil, fmt.Errorf("guest reservation has no valid UUID")
	}
	if result.Status != "RESERVED" && result.Status != "SUCCESS" {
		return nil, fmt.Errorf("guest reservation cannot authorize a platform change in state %s", result.Status)
	}
	return &result, nil
}
func (c *Client) CancelVmGuestNetworkReservation(ctx context.Context, vmID, interfaceID int64, operationUUID string, opts *RequestOpts) error {
	if _, err := uuid.ParseUUID(operationUUID); err != nil {
		return fmt.Errorf("invalid guest reservation UUID")
	}
	var result GuestNetworkState
	endpoint := scopedPath(fmt.Sprintf("/api/v2/vms/%d/local-networks/%d/guest-configuration/reservations/%s", vmID, interfaceID, operationUUID), opts)
	if err := c.Do(ctx, http.MethodDelete, endpoint, nil, &result, opts); err != nil {
		return err
	}
	if err := result.validate(); err != nil {
		return err
	}
	if err := result.RequireRecoverable(); err != nil {
		return err
	}
	if result.OperationUUID != operationUUID || result.Status != "NOT_SUBMITTED" || result.Pending == nil || *result.Pending {
		return fmt.Errorf("guest reservation cancellation is unverified; its ownership is retained")
	}
	return nil
}
