package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

const VolumeAttachmentContract = "volume-attachment-v1"

type VolumeAttachmentOperation struct {
	Contract   string `json:"contract"`
	ID         int64  `json:"id"`
	VmID       int64  `json:"vmId"`
	VolumeID   int64  `json:"volumeId"`
	Exists     *bool  `json:"exists"`
	Direction  string `json:"direction"`
	Status     string `json:"status"`
	CanRelease *bool  `json:"canRelease"`
}

func (v *VolumeAttachmentOperation) validate(vmID, volumeID, attachmentID int64) error {
	if v.Contract != VolumeAttachmentContract || v.ID <= 0 || v.VmID != vmID || v.VolumeID != volumeID || (attachmentID > 0 && v.ID != attachmentID) || v.Exists == nil {
		return fmt.Errorf("the API did not verify volume attachment identity; existing ownership was preserved")
	}
	if v.Direction != "ATTACH" && v.Direction != "DETACH" {
		return fmt.Errorf("the API returned an unknown volume move direction")
	}
	switch v.Status {
	case "RESERVED", "QUEUED", "RUNNING", "UNVERIFIED", "SUCCESS", "ERROR", "NOT_SUBMITTED", "DELETED":
	default:
		return fmt.Errorf("the API returned an unknown volume attachment status")
	}
	confirmedAbsent := v.Status == "DELETED" && v.Direction == "DETACH" || v.Status == "NOT_SUBMITTED" && v.Direction == "ATTACH" && v.CanRelease != nil && *v.CanRelease
	if !*v.Exists && !confirmedAbsent {
		return fmt.Errorf("the API did not positively confirm volume attachment absence")
	}
	if *v.Exists && (v.Status == "DELETED" || v.Status == "NOT_SUBMITTED") {
		return fmt.Errorf("the API returned contradictory volume attachment absence")
	}
	return nil
}

func attachmentPath(vmID int64) string { return fmt.Sprintf("/api/v2/vms/%d/volume-attachments", vmID) }

func (c *Client) CheckVolumeAttachmentAPI(ctx context.Context, vmID int64, opts *RequestOpts) error {
	var caps struct {
		Contract              string `json:"contract"`
		ReserveBeforeSubmit   bool   `json:"reserveBeforeSubmit"`
		RecoverableSubmission bool   `json:"recoverableSubmission"`
	}
	if err := c.Do(ctx, http.MethodGet, attachmentPath(vmID)+"/capabilities", nil, &caps, opts); err != nil {
		return err
	}
	if caps.Contract != VolumeAttachmentContract || !caps.ReserveBeforeSubmit || !caps.RecoverableSubmission {
		return fmt.Errorf("the panel must support recoverable volume attachments before moving a disk")
	}
	return nil
}

func (c *Client) ReserveVolumeAttachment(ctx context.Context, vmID, volumeID int64, opts *RequestOpts) (*VolumeAttachmentOperation, error) {
	return c.ReserveVolumeAttachmentWithIdentity(ctx, vmID, volumeID, "", opts)
}
func (c *Client) ReserveVolumeAttachmentWithIdentity(ctx context.Context, vmID, volumeID int64, expected string, opts *RequestOpts) (*VolumeAttachmentOperation, error) {
	return c.volumeAttachmentOperation(ctx, http.MethodPost, vmID, volumeID, 0, "/reservations", AttachVolumeRequest{VolumeID: volumeID, ExpectedSourceVolid: expected}, opts)
}
func (c *Client) GetVolumeAttachment(ctx context.Context, vmID, volumeID, id int64, opts *RequestOpts) (*VolumeAttachmentOperation, error) {
	return c.volumeAttachmentOperation(ctx, http.MethodGet, vmID, volumeID, id, fmt.Sprintf("/%d", id), nil, opts)
}
func (c *Client) SubmitVolumeAttachment(ctx context.Context, vmID, volumeID, id int64, opts *RequestOpts) (*VolumeAttachmentOperation, error) {
	return c.volumeAttachmentOperation(ctx, http.MethodPost, vmID, volumeID, id, fmt.Sprintf("/%d/submit", id), nil, opts)
}
func (c *Client) DeleteVolumeAttachment(ctx context.Context, vmID, volumeID, id int64, opts *RequestOpts) (*VolumeAttachmentOperation, error) {
	return c.DeleteVolumeAttachmentWithIdentity(ctx, vmID, volumeID, id, "", opts)
}
func (c *Client) DeleteVolumeAttachmentWithIdentity(ctx context.Context, vmID, volumeID, id int64, expected string, opts *RequestOpts) (*VolumeAttachmentOperation, error) {
	suffix := fmt.Sprintf("/%d", id)
	if expected != "" {
		suffix += "?expectedSourceVolid=" + url.QueryEscape(expected)
	}
	return c.volumeAttachmentOperation(ctx, http.MethodDelete, vmID, volumeID, id, suffix, nil, opts)
}
func (c *Client) volumeAttachmentOperation(ctx context.Context, method string, vmID, volumeID, id int64, suffix string, body any, opts *RequestOpts) (*VolumeAttachmentOperation, error) {
	var result VolumeAttachmentOperation
	if err := c.Do(ctx, method, attachmentPath(vmID)+suffix, body, &result, opts); err != nil {
		if id > 0 && (method == http.MethodGet || method == http.MethodDelete) && IsVmNotFound(err) {
			absent := false
			return &VolumeAttachmentOperation{Contract: VolumeAttachmentContract, ID: id, VmID: vmID, VolumeID: volumeID, Exists: &absent, Direction: "DETACH", Status: "DELETED"}, nil
		}
		return nil, err
	}
	if err := result.validate(vmID, volumeID, id); err != nil {
		return nil, err
	}
	return &result, nil
}
