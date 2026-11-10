package resources

import (
	"context"
	"fmt"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"time"

	"terraform-provider-prodata/internal/client"
)

func requireGuestNetworkSupport(ctx context.Context, c *client.Client, vmID int64, opts *client.RequestOpts) error {
	return requireGuestNetworkCapabilities(ctx, c, vmID, opts, true)
}

func requireGuestNetworkDrainSupport(ctx context.Context, c *client.Client, vmID int64, opts *client.RequestOpts) error {
	return requireGuestNetworkCapabilities(ctx, c, vmID, opts, false)
}

func requireGuestNetworkCapabilities(ctx context.Context, c *client.Client, vmID int64, opts *client.RequestOpts, newAction bool) error {
	state, err := c.GetVmGuestNetworkCapabilities(ctx, vmID, opts)
	if err != nil {
		return err
	}
	if err = state.RequireRecoverable(); err != nil {
		return err
	}
	if !*state.Supported {
		return fmt.Errorf("guest networking cannot be automated: %s", state.Reason)
	}
	if newAction && (state.RecoverablePowerChildren == nil || !*state.RecoverablePowerChildren) {
		return fmt.Errorf("the panel must support recoverable power children before changing guest networking")
	}
	return nil
}

// Unknown observations preserve the last verified value. Only explicit proof is drift.
func confirmedGuestNetwork(s *client.GuestNetworkState) bool {
	return s != nil && s.RequireRecoverable() == nil && s.Supported != nil && *s.Supported && s.Verified != nil && *s.Verified && s.CloudInitDisabled != nil && !*s.CloudInitDisabled && (s.Pending == nil || !*s.Pending)
}
func applyGuestNetwork(ctx context.Context, c *client.Client, vmID, interfaceID int64, req client.ConfigureGuestNetworkRequest, opts *client.RequestOpts, poll time.Duration) error {
	_, err := runGuestNetwork(ctx, c, vmID, interfaceID, req, opts, poll, false)
	return err
}
func releaseGuestNetwork(ctx context.Context, c *client.Client, vmID, interfaceID int64, req client.ConfigureGuestNetworkRequest, opts *client.RequestOpts, poll time.Duration) error {
	_, err := runGuestNetwork(ctx, c, vmID, interfaceID, req, opts, poll, true)
	return err
}
func runGuestNetwork(ctx context.Context, c *client.Client, vmID, interfaceID int64, req client.ConfigureGuestNetworkRequest, opts *client.RequestOpts, poll time.Duration, release bool) (*client.GuestNetworkState, error) {
	// Validate once: authorization, missing targets and unsupported guests cannot be cured by polling.
	// Releasing an existing NIC or its exact owned route is drain, not new admission.
	ownedRouteCleanup := req.ConfigureDefaultRoute && req.Gateway == nil && req.ExpectedGuestGateway != nil && *req.ExpectedGuestGateway != ""
	newAction := !release && !ownedRouteCleanup
	if err := requireGuestNetworkCapabilities(ctx, c, vmID, opts, newAction); err != nil {
		return nil, err
	}
	var state *client.GuestNetworkState
	var err error
	if release {
		state, err = c.ReleaseVmGuestNetwork(ctx, vmID, interfaceID, req, opts)
	} else {
		state, err = c.ConfigureVmGuestNetwork(ctx, vmID, interfaceID, req, opts)
	}
	// A lost POST answer is resolved only by observing the persisted NIC/spec operation.
	if err != nil {
		state, err = c.GetVmGuestNetworkState(ctx, vmID, interfaceID, req, opts)
		if err != nil {
			return nil, err
		}
	}
	knownUUID := ""
	if state != nil {
		knownUUID = state.OperationUUID
	}
	for {
		if state == nil {
			return nil, fmt.Errorf("the guest operation returned no outcome")
		}
		if err := state.RequireRecoverable(); err != nil {
			return nil, err
		}
		if knownUUID != "" && state.OperationUUID != knownUUID {
			return nil, fmt.Errorf("the guest operation identity changed; ownership is retained")
		}
		if confirmedGuestNetwork(state) && (release && state.Released != nil && *state.Released || !release && state.Configured != nil && *state.Configured) {
			return state, nil
		}
		if state.Pending == nil || !*state.Pending {
			return nil, fmt.Errorf("guest operation was not verified: %s", state.Reason)
		}
		if state.OperationUUID == "" {
			return nil, fmt.Errorf("guest operation has no durable identity")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(poll):
		}
		state, err = c.GetVmGuestNetworkState(ctx, vmID, interfaceID, req, opts)
		if err != nil {
			return nil, err
		}
	}
}

func gatewayGuestRequest(ctx context.Context, c *client.Client, vmID, interfaceID, networkID int64, gateway, expectedGuestGateway *string, opts *client.RequestOpts) (client.ConfigureGuestNetworkRequest, error) {
	nics, err := c.GetVmLocalNetworks(ctx, vmID, opts)
	if err != nil {
		return client.ConfigureGuestNetworkRequest{}, err
	}
	for _, nic := range nics {
		if nic.ID != interfaceID {
			continue
		}
		if nic.NetworkID == nil || *nic.NetworkID != networkID || nic.IP == nil || nic.MAC == nil || *nic.IP == "" || *nic.MAC == "" {
			return client.ConfigureGuestNetworkRequest{}, fmt.Errorf("the selected local interface has no verified network, address or MAC")
		}
		return client.ConfigureGuestNetworkRequest{ExpectedMAC: *nic.MAC, ExpectedIP: *nic.IP, ConfigureDefaultRoute: true, Gateway: gateway, ExpectedGuestGateway: expectedGuestGateway}, nil
	}
	return client.ConfigureGuestNetworkRequest{}, fmt.Errorf("the selected interface is absent; guest configuration was refused")
}

func (r *VmGatewayResource) configureGuest(ctx context.Context, data *VmGatewayResourceModel, gateway, expectedGuestGateway *string) error {
	request, err := gatewayGuestRequest(ctx, r.client, data.VmID.ValueInt64(), data.InterfaceID.ValueInt64(), data.NetworkID.ValueInt64(), gateway, expectedGuestGateway, r.opts(data))
	if err != nil {
		return err
	}
	state, err := runGuestNetwork(ctx, r.client, data.VmID.ValueInt64(), data.InterfaceID.ValueInt64(), request, r.opts(data), r.pollInterval(), false)
	if err == nil && state.OperationUUID != "" {
		data.GuestOperationUUID = types.StringValue(state.OperationUUID)
		data.GuestOperationStatus = types.StringValue(state.Status)
	}
	if err == nil {
		data.GuestOwnedGateway = nullableString(gateway)
	}
	return err
}
