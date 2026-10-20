package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// VmLocalNetwork is one local network interface of a VM, as GET /api/v2/vms/{id}/local-networks
// lists it. ID is the identity of the interface (it is stable while the interface stays attached);
// the interface's index inside the hypervisor is not part of the API.
//
// The fields the panel may not know are pointers: NetworkID and NetworkName are null for a legacy
// row that was never tied to a network, IP when the panel has no address for the interface, MAC
// for an interface that was not created through the v2 API, Gateway unless one was set on the
// interface (the v2 API never sets it).
type VmLocalNetwork struct {
	ID          int64   `json:"id"`
	NetworkID   *int64  `json:"networkId"`
	NetworkName *string `json:"networkName"`
	IP          *string `json:"ip"`
	MAC         *string `json:"mac"`
	Gateway     *string `json:"gateway"`
	// Primary marks an interface that cannot be detached through the API: the one in the network
	// the VM was created in, and any interface with a gateway.
	Primary bool `json:"primary"`
}

// AttachLocalNetworkRequest is the body of POST /api/v2/vms/{id}/local-networks. An empty IP asks
// the panel for the next free address of the network.
type AttachLocalNetworkRequest struct {
	NetworkID int64  `json:"networkId"`
	IP        string `json:"ip,omitempty"`
}

// AttachedLocalNetwork is the answer to an attach: the new interface, and what is left to do for
// the address to work. The panel does not restart the VM itself.
type AttachedLocalNetwork struct {
	VmLocalNetwork

	// RequiresRestart is true when the VM was running, so the interface or its address may not
	// take effect before the VM is restarted.
	RequiresRestart bool `json:"requiresRestart"`
	// RequiresGuestConfiguration is true when the VM has no cloud-init, so nothing configures the
	// address inside the guest.
	RequiresGuestConfiguration bool `json:"requiresGuestConfiguration"`
}

// GetVmLocalNetworks lists the local network interfaces of a VM, ordered by id. A public IP is not
// among them.
func (c *Client) GetVmLocalNetworks(ctx context.Context, vmID int64, opts *RequestOpts) ([]VmLocalNetwork, error) {
	var nics []VmLocalNetwork
	path := scopedPath(fmt.Sprintf("/api/v2/vms/%d/local-networks", vmID), opts)
	if err := c.Do(ctx, http.MethodGet, path, nil, &nics, opts); err != nil {
		return nil, err
	}
	return nics, nil
}

// AttachLocalNetwork adds a local network interface to a VM that is RUNNING or STOPPED.
func (c *Client) AttachLocalNetwork(ctx context.Context, vmID int64, req AttachLocalNetworkRequest, opts *RequestOpts) (*AttachedLocalNetwork, error) {
	var attached AttachedLocalNetwork
	path := scopedPath(fmt.Sprintf("/api/v2/vms/%d/local-networks", vmID), opts)
	if err := c.Do(ctx, http.MethodPost, path, req, &attached, opts); err != nil {
		return nil, err
	}
	return &attached, nil
}

// DetachLocalNetwork removes a local network interface from a VM, by its id.
func (c *Client) DetachLocalNetwork(ctx context.Context, vmID, interfaceID int64, opts *RequestOpts) error {
	path := scopedPath(fmt.Sprintf("/api/v2/vms/%d/local-networks/%d", vmID, interfaceID), opts)
	return c.Do(ctx, http.MethodDelete, path, nil, nil, opts)
}

// scopedPath adds the region and project of opts, when set, to path as query parameters.
func scopedPath(path string, opts *RequestOpts) string {
	params := url.Values{}
	if opts != nil {
		if opts.Region != "" {
			params.Set("region", opts.Region)
		}
		if opts.ProjectTag != "" {
			params.Set("projectTag", opts.ProjectTag)
		}
	}
	if len(params) == 0 {
		return path
	}
	return path + "?" + params.Encode()
}
