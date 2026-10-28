package client

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// ChangedVmDNS is the actual or forecast update of a VM's cloud-init DNS.
type ChangedVmDNS struct {
	Nameserver                 string
	Changed                    bool
	RequiresGuestConfiguration bool
}

func (c *Client) ChangeVmDNS(ctx context.Context, vmID int64, nameserver string, dryRun bool, opts *RequestOpts) (*ChangedVmDNS, error) {
	var wire struct {
		Nameserver                 *string `json:"nameserver"`
		Changed                    *bool   `json:"changed"`
		RequiresGuestConfiguration *bool   `json:"requiresGuestConfiguration"`
	}
	path := scopedPath(fmt.Sprintf("/api/v2/vms/%d/dns", vmID), opts)
	if dryRun {
		if strings.Contains(path, "?") {
			path += "&dryRun=true"
		} else {
			path += "?dryRun=true"
		}
	}
	if err := retryNetworkSettingsBusy(ctx, func() error {
		return c.Do(ctx, http.MethodPut, path, map[string]string{"nameserver": nameserver}, &wire, opts)
	}); err != nil {
		return nil, err
	}
	if wire.Nameserver == nil || *wire.Nameserver != nameserver || wire.Changed == nil || wire.RequiresGuestConfiguration == nil {
		return nil, fmt.Errorf("the panel returned an incomplete or mismatched VM DNS update")
	}
	return &ChangedVmDNS{Nameserver: *wire.Nameserver, Changed: *wire.Changed,
		RequiresGuestConfiguration: *wire.RequiresGuestConfiguration}, nil
}

// VmDNS is the DNS configured for a VM in Proxmox, not a measurement inside the guest.
type VmDNS struct {
	Nameserver                 *string
	Pending                    bool
	ReconcileRequired          bool
	RequiresGuestConfiguration bool
}

// GetVmDNS reads the DNS from the dedicated API. An old panel with no route, or an incomplete
// reply, is an error: neither means the VM has no DNS or should be removed from Terraform state.
func (c *Client) GetVmDNS(ctx context.Context, vmID int64, opts *RequestOpts) (*VmDNS, error) {
	var wire struct {
		Nameserver                 *string `json:"nameserver"`
		Configured                 *bool   `json:"configured"`
		Pending                    *bool   `json:"pending"`
		ReconcileRequired          *bool   `json:"reconcileRequired"`
		RequiresGuestConfiguration *bool   `json:"requiresGuestConfiguration"`
	}
	path := scopedPath(fmt.Sprintf("/api/v2/vms/%d/dns", vmID), opts)
	if err := c.Do(ctx, http.MethodGet, path, nil, &wire, opts); err != nil {
		return nil, err
	}
	if wire.Configured == nil || wire.Pending == nil || wire.ReconcileRequired == nil || wire.RequiresGuestConfiguration == nil ||
		*wire.Configured != (wire.Nameserver != nil) {
		return nil, fmt.Errorf("the panel returned an incomplete VM DNS response")
	}
	return &VmDNS{
		Nameserver:                 wire.Nameserver,
		Pending:                    *wire.Pending,
		ReconcileRequired:          *wire.ReconcileRequired,
		RequiresGuestConfiguration: *wire.RequiresGuestConfiguration,
	}, nil
}
