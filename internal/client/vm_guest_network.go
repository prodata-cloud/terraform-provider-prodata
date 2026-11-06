package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type GuestNetworkState struct {
	Supported  *bool  `json:"supported"`
	Configured *bool  `json:"configured"`
	Driver     string `json:"driver"`
	Reason     string `json:"reason"`
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

func (c *Client) ConfigureVmGuestNetwork(ctx context.Context, vmID, interfaceID int64, req ConfigureGuestNetworkRequest, opts *RequestOpts) (*GuestNetworkState, error) {
	var result GuestNetworkState
	path := scopedPath(fmt.Sprintf("/api/v2/vms/%d/local-networks/%d/guest-configuration", vmID, interfaceID), opts)
	if err := c.Do(ctx, http.MethodPost, path, req, &result, opts); err != nil {
		return nil, err
	}
	if err := result.validate(); err != nil {
		return nil, err
	}
	if !*result.Supported || !*result.Configured {
		return nil, fmt.Errorf("guest network configuration was not confirmed: %s", result.Reason)
	}
	return &result, nil
}
