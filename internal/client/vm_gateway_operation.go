package client

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/hashicorp/go-uuid"
	"net/http"
	"strconv"
	"strings"
)

const GatewayOperationContract = "vm-gateway-operation-v1"

type GatewayOperation struct {
	Contract           string            `json:"contract"`
	RequestUUID        string            `json:"requestUuid"`
	VmID               int64             `json:"vmId"`
	NetworkID          int64             `json:"networkId"`
	InterfaceID        *int64            `json:"interfaceId"`
	Status             string            `json:"status"`
	Exists             *bool             `json:"exists"`
	CanRelease         *bool             `json:"canRelease"`
	Result             *ChangedVmGateway `json:"result"`
	Operation          string            `json:"operation"`
	GuestOperationUUID string            `json:"guestOperationUuid"`
}

func (c *Client) RequireGatewayOperations(ctx context.Context, vmID int64, opts *RequestOpts) error {
	var caps struct {
		Contract string `json:"contract"`
		Reserve  bool   `json:"reserveBeforeSubmit"`
		Recover  bool   `json:"recoverableSubmission"`
		Power    bool   `json:"powerChildrenBeforeSubmit"`
		Cancel   bool   `json:"cancelTombstone"`
	}
	if err := c.Do(ctx, http.MethodGet, scopedPath(fmt.Sprintf("/api/v2/vms/%d/gateway-operations/capabilities", vmID), opts), nil, &caps, opts); err != nil {
		return err
	}
	if caps.Contract != GatewayOperationContract || !caps.Reserve || !caps.Recover || !caps.Power || !caps.Cancel {
		return fmt.Errorf("recoverable gateway operations are unavailable")
	}
	return nil
}
func (c *Client) ReserveGatewayOperation(ctx context.Context, vmID int64, uuid string, body ChangeVmGatewayRequest, opts *RequestOpts) (*GatewayOperation, error) {
	out, err := c.gatewayOperation(ctx, http.MethodPost, vmID, body.NetworkID, uuid, "/reservations", body, opts)
	if err == nil && !gatewayOperationCancelled(out) {
		if out.Result == nil || out.Operation != gatewayOperationKind(body.Clear) || body.ExpectedInterfaceID != nil && (out.InterfaceID == nil || *out.InterfaceID != *body.ExpectedInterfaceID) || !body.Clear && (body.Gateway == nil || out.Result.Gateway == nil || *body.Gateway != *out.Result.Gateway) {
			return nil, fmt.Errorf("gateway reservation has another immutable specification")
		}
	}
	return out, err
}
func gatewayOperationKind(clearRoute bool) string {
	if clearRoute {
		return "CLEAR_GATEWAY"
	}
	return "SET_GATEWAY"
}
func gatewayOperationCancelled(o *GatewayOperation) bool {
	return o != nil && (o.Status == "NOT_SUBMITTED" || o.Status == "DELETED") && o.CanRelease != nil && *o.CanRelease && o.Exists != nil && !*o.Exists
}
func (c *Client) ReadGatewayOperation(ctx context.Context, vmID, networkID int64, uuid string, opts *RequestOpts) (*GatewayOperation, error) {
	return c.gatewayOperation(ctx, http.MethodGet, vmID, networkID, uuid, "", nil, opts)
}
func (c *Client) SubmitGatewayOperation(ctx context.Context, vmID, networkID int64, uuid string, opts *RequestOpts) (*GatewayOperation, error) {
	return c.gatewayOperation(ctx, http.MethodPost, vmID, networkID, uuid, "/submit", nil, opts)
}
func (c *Client) CancelGatewayOperation(ctx context.Context, vmID, networkID int64, uuid string, opts *RequestOpts) (*GatewayOperation, error) {
	return c.gatewayOperation(ctx, http.MethodDelete, vmID, networkID, uuid, "", nil, opts)
}
func (c *Client) gatewayOperation(ctx context.Context, method string, vmID, networkID int64, uuid, suffix string, body any, opts *RequestOpts) (*GatewayOperation, error) {
	if !canonicalGatewayUUID(uuid) {
		return nil, fmt.Errorf("gateway operation requires a canonical UUID")
	}
	path := scopedPath(fmt.Sprintf("/api/v2/vms/%d/gateway-operations/%s%s", vmID, uuid, suffix), opts)
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	path += sep + "networkId=" + strconv.FormatInt(networkID, 10)
	var out GatewayOperation
	if err := c.Do(ctx, method, path, body, &out, opts); err != nil {
		return nil, err
	}
	if out.Contract != GatewayOperationContract || out.RequestUUID != uuid || out.VmID != vmID || out.NetworkID != networkID || out.CanRelease == nil {
		return nil, fmt.Errorf("gateway operation identity is unverified")
	}
	switch out.Status {
	case "RESERVED", "SUBMITTING":
		if out.Exists != nil || *out.CanRelease {
			return nil, fmt.Errorf("pending gateway operation cannot prove presence or absence")
		}
	case "SUCCESS":
		if out.Exists == nil || !*out.Exists || *out.CanRelease || out.Result == nil {
			return nil, fmt.Errorf("gateway operation completion is unverified")
		}
	case "NOT_SUBMITTED", "DELETED":
		if !gatewayOperationCancelled(&out) {
			return nil, fmt.Errorf("gateway cancellation is unverified")
		}
	default:
		return nil, fmt.Errorf("unknown gateway operation status %q", out.Status)
	}
	if out.Status != "NOT_SUBMITTED" && out.Status != "DELETED" && (out.InterfaceID == nil || *out.InterfaceID <= 0 || out.Result == nil || out.Result.InterfaceID != *out.InterfaceID || out.Result.NetworkID != networkID || (out.Operation != "SET_GATEWAY" && out.Operation != "CLEAR_GATEWAY") || out.Operation == "SET_GATEWAY" && out.Result.Gateway == nil || out.Operation == "CLEAR_GATEWAY" && out.Result.Changed && out.Result.Gateway != nil) {
		return nil, fmt.Errorf("gateway result is incomplete or mismatched")
	}
	return &out, nil
}

func canonicalGatewayUUID(value string) bool {
	_, err := uuid.ParseUUID(value)
	return err == nil && len(value) == 36 && strings.ToLower(value) == value
}
func (out *ChangedVmGateway) UnmarshalJSON(data []byte) error {
	var w struct {
		InterfaceID *int64  `json:"interfaceId"`
		NetworkID   *int64  `json:"networkId"`
		Gateway     *string `json:"gateway"`
		Changed     *bool   `json:"changed"`
		Other       *bool   `json:"previousGatewayOnAnotherInterface"`
		Guest       *bool   `json:"requiresGuestConfiguration"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	if w.InterfaceID == nil || *w.InterfaceID <= 0 || w.NetworkID == nil || *w.NetworkID <= 0 || w.Changed == nil || w.Other == nil || w.Guest == nil {
		return fmt.Errorf("gateway result lacks explicit verification fields")
	}
	*out = ChangedVmGateway{InterfaceID: *w.InterfaceID, NetworkID: *w.NetworkID, Gateway: w.Gateway, Changed: *w.Changed, PreviousGatewayOnAnotherInterface: *w.Other, RequiresGuestConfiguration: *w.Guest}
	return nil
}
