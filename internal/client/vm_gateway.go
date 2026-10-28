package client

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// VmGateway describes the default gateway on a selected local interface. It reports the
// hypervisor's cloud-init configuration rather than the route inside the guest.
type VmGateway struct {
	InterfaceID                int64
	NetworkID                  int64
	Gateway                    *string
	Pending                    bool
	ReconcileRequired          bool
	OtherGatewayActive         bool
	RequiresGuestConfiguration bool
}

type ChangeVmGatewayRequest struct {
	NetworkID           int64   `json:"networkId"`
	Gateway             *string `json:"gateway,omitempty"`
	ExpectedGateway     *string `json:"expectedGateway,omitempty"`
	Clear               bool    `json:"clear"`
	ExpectedInterfaceID *int64  `json:"expectedInterfaceId,omitempty"`
}

type ChangedVmGateway struct {
	InterfaceID                       int64
	NetworkID                         int64
	Gateway                           *string
	Changed                           bool
	PreviousGatewayOnAnotherInterface bool
	RequiresGuestConfiguration        bool
}

func (c *Client) ChangeVmGateway(ctx context.Context, vmID int64, request ChangeVmGatewayRequest, dryRun bool, opts *RequestOpts) (*ChangedVmGateway, error) {
	var wire struct {
		InterfaceID                       *int64  `json:"interfaceId"`
		NetworkID                         *int64  `json:"networkId"`
		Gateway                           *string `json:"gateway"`
		Changed                           *bool   `json:"changed"`
		PreviousGatewayOnAnotherInterface *bool   `json:"previousGatewayOnAnotherInterface"`
		RequiresGuestConfiguration        *bool   `json:"requiresGuestConfiguration"`
	}
	path := scopedPath(fmt.Sprintf("/api/v2/vms/%d/gateway", vmID), opts)
	if dryRun {
		if strings.Contains(path, "?") {
			path += "&dryRun=true"
		} else {
			path += "?dryRun=true"
		}
	}
	if err := retryNetworkSettingsBusy(ctx, func() error {
		return c.Do(ctx, http.MethodPut, path, request, &wire, opts)
	}); err != nil {
		return nil, err
	}
	if wire.InterfaceID == nil || *wire.InterfaceID <= 0 || wire.NetworkID == nil || *wire.NetworkID != request.NetworkID ||
		(request.ExpectedInterfaceID != nil && *wire.InterfaceID != *request.ExpectedInterfaceID) ||
		wire.Changed == nil || wire.PreviousGatewayOnAnotherInterface == nil || wire.RequiresGuestConfiguration == nil ||
		(!request.Clear && (wire.Gateway == nil || request.Gateway == nil || *wire.Gateway != *request.Gateway)) ||
		(request.Clear && *wire.Changed && wire.Gateway != nil) {
		return nil, fmt.Errorf("the panel returned an incomplete or mismatched VM gateway update")
	}
	return &ChangedVmGateway{InterfaceID: *wire.InterfaceID, NetworkID: *wire.NetworkID, Gateway: wire.Gateway,
		Changed: *wire.Changed, PreviousGatewayOnAnotherInterface: *wire.PreviousGatewayOnAnotherInterface,
		RequiresGuestConfiguration: *wire.RequiresGuestConfiguration}, nil
}

// GetVmGateway reads a VM's route for one local network. An error, including a missing route on an
// older panel, must not be interpreted as an absent VM or a cleared gateway.
func (c *Client) GetVmGateway(ctx context.Context, vmID, networkID int64, opts *RequestOpts) (*VmGateway, error) {
	var wire struct {
		InterfaceID                *int64  `json:"interfaceId"`
		NetworkID                  *int64  `json:"networkId"`
		Gateway                    *string `json:"gateway"`
		Present                    *bool   `json:"present"`
		Pending                    *bool   `json:"pending"`
		ReconcileRequired          *bool   `json:"reconcileRequired"`
		OtherGatewayActive         *bool   `json:"otherGatewayActive"`
		RequiresGuestConfiguration *bool   `json:"requiresGuestConfiguration"`
	}
	path := scopedPath(fmt.Sprintf("/api/v2/vms/%d/gateway", vmID), opts)
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	path += separator + "networkId=" + strconv.FormatInt(networkID, 10)
	if err := c.Do(ctx, http.MethodGet, path, nil, &wire, opts); err != nil {
		return nil, err
	}
	if wire.InterfaceID == nil || *wire.InterfaceID <= 0 || wire.NetworkID == nil || *wire.NetworkID != networkID ||
		wire.Present == nil || wire.Pending == nil || wire.ReconcileRequired == nil || wire.OtherGatewayActive == nil || wire.RequiresGuestConfiguration == nil ||
		*wire.Present != (wire.Gateway != nil) {
		return nil, fmt.Errorf("the panel returned an incomplete or mismatched VM gateway response")
	}
	return &VmGateway{
		InterfaceID:                *wire.InterfaceID,
		NetworkID:                  *wire.NetworkID,
		Gateway:                    wire.Gateway,
		Pending:                    *wire.Pending,
		ReconcileRequired:          *wire.ReconcileRequired,
		OtherGatewayActive:         *wire.OtherGatewayActive,
		RequiresGuestConfiguration: *wire.RequiresGuestConfiguration,
	}, nil
}
