package resources

import (
	"context"
	"fmt"
	"time"

	"terraform-provider-prodata/internal/client"
)

func requireGuestNetworkSupport(ctx context.Context, c *client.Client, vmID int64, opts *client.RequestOpts) error {
	state, err := c.GetVmGuestNetworkCapabilities(ctx, vmID, opts)
	if err != nil {
		return err
	}
	if !*state.Supported {
		return fmt.Errorf("guest networking cannot be automated: %s", state.Reason)
	}
	return nil
}

// A power cycle can bring the VM up before its agent. Poll only the read-only
// preflight; the configuration POST itself is sent once and must be confirmed.
func applyGuestNetwork(ctx context.Context, c *client.Client, vmID, interfaceID int64, req client.ConfigureGuestNetworkRequest, opts *client.RequestOpts, poll time.Duration) error {
	var last error
	for attempt := 0; attempt < 24; attempt++ {
		last = requireGuestNetworkSupport(ctx, c, vmID, opts)
		if last == nil {
			break
		}
		if attempt == 23 {
			return last
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
	_, err := c.ConfigureVmGuestNetwork(ctx, vmID, interfaceID, req, opts)
	return err
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
	return applyGuestNetwork(ctx, r.client, data.VmID.ValueInt64(), data.InterfaceID.ValueInt64(), request, r.opts(data), r.pollInterval())
}
