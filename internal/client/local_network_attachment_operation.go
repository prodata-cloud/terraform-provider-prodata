package client

import (
	"context"
	"fmt"
	"net"
	"net/http"
)

const LocalNetworkAttachmentContract = "local-network-attachment-v1"

type LocalNetworkAttachmentOperation struct {
	VmLocalNetwork
	Contract                   string  `json:"contract"`
	VmID                       int64   `json:"vmId"`
	Exists                     *bool   `json:"exists"`
	Operation                  string  `json:"operation"`
	Status                     string  `json:"status"`
	CanRelease                 *bool   `json:"canRelease"`
	NetIndex                   *int    `json:"netIndex"`
	PveFullName                *string `json:"pveFullName"`
	RequiresRestart            bool    `json:"requiresRestart"`
	RequiresGuestConfiguration bool    `json:"requiresGuestConfiguration"`
}

func localAttachmentPath(vmID int64) string {
	return fmt.Sprintf("/api/v2/vms/%d/local-network-attachments", vmID)
}
func (v *LocalNetworkAttachmentOperation) validate(vmID, networkID, id int64, strict bool) error {
	if v.Contract != LocalNetworkAttachmentContract || v.ID < 1 || id > 0 && v.ID != id || v.VmID != vmID || v.Exists == nil || v.CanRelease == nil {
		return fmt.Errorf("the API did not verify the reserved local interface identity")
	}
	switch v.Operation {
	case "ATTACH_LOCAL_NIC", "DETACH_LOCAL_NIC", "GUEST_NETWORK":
	default:
		return fmt.Errorf("unknown local interface operation")
	}
	switch v.Status {
	case "RESERVED", "QUEUED", "PROCESSING", "UNVERIFIED", "SUCCESS", "ERROR", "NOT_SUBMITTED", "DELETED":
	default:
		return fmt.Errorf("unknown local interface operation status")
	}
	if v.NetworkID != nil && (networkID > 0 && *v.NetworkID != networkID || *v.NetworkID < 1) {
		return fmt.Errorf("the reserved interface refers to another network")
	}
	if !*v.Exists {
		confirmedAbsent := v.Status == "DELETED" && v.Operation == "DETACH_LOCAL_NIC" || v.Status == "NOT_SUBMITTED" && v.Operation == "ATTACH_LOCAL_NIC" && *v.CanRelease
		if !confirmedAbsent {
			return fmt.Errorf("local interface absence is unproven")
		}
		return nil
	}
	if v.Status == "DELETED" || v.Operation == "ATTACH_LOCAL_NIC" && v.Status == "NOT_SUBMITTED" {
		return fmt.Errorf("contradictory local interface existence")
	}
	if v.NetworkID == nil {
		return fmt.Errorf("the reserved interface is not tied to a local network")
	}
	if strict && (v.IP == nil || net.ParseIP(*v.IP) == nil || v.MAC == nil) {
		return fmt.Errorf("the reserved local interface has no stable address or MAC")
	}
	if strict {
		if _, err := net.ParseMAC(*v.MAC); err != nil {
			return fmt.Errorf("invalid reserved interface MAC")
		}
	}
	if v.Status == "SUCCESS" && (v.NetIndex == nil || *v.NetIndex < 0 || v.PveFullName == nil || *v.PveFullName != fmt.Sprintf("net%d", *v.NetIndex)) {
		return fmt.Errorf("the API did not verify the physical local interface")
	}
	return nil
}
func (c *Client) CheckLocalNetworkAttachmentAPI(ctx context.Context, vmID int64, opts *RequestOpts) error {
	var caps struct {
		Contract                string `json:"contract"`
		ReserveBeforeSubmit     bool   `json:"reserveBeforeSubmit"`
		RecoverableSubmission   bool   `json:"recoverableSubmission"`
		StrictInterfaceIdentity bool   `json:"strictInterfaceIdentity"`
	}
	if err := c.Do(ctx, http.MethodGet, scopedPath(localAttachmentPath(vmID)+"/capabilities", opts), nil, &caps, opts); err != nil {
		return err
	}
	if caps.Contract != LocalNetworkAttachmentContract || !caps.ReserveBeforeSubmit || !caps.RecoverableSubmission || !caps.StrictInterfaceIdentity {
		return fmt.Errorf("the panel must support recoverable local interfaces before changing the VM network")
	}
	return nil
}
func (c *Client) ReserveLocalNetworkAttachment(ctx context.Context, vmID int64, request AttachLocalNetworkRequest, opts *RequestOpts) (*LocalNetworkAttachmentOperation, error) {
	v, err := c.localNetworkAttachmentOperation(ctx, http.MethodPost, vmID, request.NetworkID, 0, "/reservations", request, opts)
	if err == nil && request.IP != "" && *v.Exists && (v.IP == nil || *v.IP != request.IP) {
		return nil, fmt.Errorf("the reservation changed the explicitly requested interface address")
	}
	return v, err
}
func (c *Client) GetLocalNetworkAttachment(ctx context.Context, vmID, networkID, id int64, opts *RequestOpts) (*LocalNetworkAttachmentOperation, error) {
	return c.localNetworkAttachmentOperation(ctx, http.MethodGet, vmID, networkID, id, fmt.Sprintf("/%d", id), nil, opts)
}
func (c *Client) SubmitLocalNetworkAttachment(ctx context.Context, vmID, networkID, id int64, opts *RequestOpts) (*LocalNetworkAttachmentOperation, error) {
	return c.localNetworkAttachmentOperation(ctx, http.MethodPost, vmID, networkID, id, fmt.Sprintf("/%d/submit", id), nil, opts)
}
func (c *Client) DeleteLocalNetworkAttachment(ctx context.Context, vmID, networkID, id int64, opts *RequestOpts) (*LocalNetworkAttachmentOperation, error) {
	return c.localNetworkAttachmentOperation(ctx, http.MethodDelete, vmID, networkID, id, fmt.Sprintf("/%d", id), nil, opts)
}
func (c *Client) localNetworkAttachmentOperation(ctx context.Context, method string, vmID, networkID, id int64, suffix string, body any, opts *RequestOpts) (*LocalNetworkAttachmentOperation, error) {
	var v LocalNetworkAttachmentOperation
	if err := c.Do(ctx, method, scopedPath(localAttachmentPath(vmID)+suffix, opts), body, &v, opts); err != nil {
		return nil, err
	}

	if err := v.validate(vmID, networkID, id, method != http.MethodGet); err != nil {
		if v.validate(vmID, networkID, id, false) == nil {
			return &v, err
		}
		return nil, err
	}
	return &v, nil
}
