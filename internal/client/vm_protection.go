package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

const vmProtectionContract = "vm-protection-reservation-v1"

// CheckVmProtectionCapabilities fails before any mutation when the backend lacks durable
// reservation semantics. Old endpoints must not be mistaken for safe two-step creation.
func (c *Client) CheckVmProtectionCapabilities(ctx context.Context, opts *RequestOpts) error {
	var caps struct {
		Contract string `json:"contract"`
		Reserve  bool   `json:"reserveBeforeSubmit"`
		Single   bool   `json:"singleSubmit"`
		Restore  bool   `json:"restoreIdentityBeforeSubmit"`
	}
	if err := c.Do(ctx, http.MethodGet, "/api/v2/vm-protection/capabilities", nil, &caps, opts); err != nil {
		return fmt.Errorf("VM protection reservation API is unavailable: %w", err)
	}
	if caps.Contract != vmProtectionContract || !caps.Reserve || !caps.Single || !caps.Restore {
		return fmt.Errorf("backend does not confirm durable single-submit VM protection; no resource was created")
	}
	return nil
}

// VmProtection identifies a panel backup, snapshot, or backup schedule.
// Present=false is a verified domain tombstone; an unknown HTTP route is never one.
type VmProtection struct {
	Contract   string `json:"contract"`
	ID         int64  `json:"id"`
	VmID       int64  `json:"vmId"`
	Present    bool   `json:"present"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Mode       string `json:"mode"`
	IncludeRAM bool   `json:"includeRam"`
	Cron       string `json:"cron"`
}

// UnmarshalJSON refuses a missing/null liveness field rather than treating a malformed
// response as permission to remove a resource from Terraform state.
func (v *VmProtection) UnmarshalJSON(data []byte) error {
	type wire VmProtection
	var check struct {
		Present *bool `json:"present"`
	}
	if err := json.Unmarshal(data, &check); err != nil {
		return err
	}
	if check.Present == nil {
		return fmt.Errorf("VM protection response omitted its liveness confirmation")
	}
	var out wire
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	*v = VmProtection(out)
	return nil
}

type VmProtectionRequest struct {
	Name       string `json:"name"`
	Mode       string `json:"mode,omitempty"`
	IncludeRAM *bool  `json:"includeRam,omitempty"`
	Cron       string `json:"cron,omitempty"`
}

func protectionPath(vmID int64, kind string, id int64) (string, error) {
	if vmID < 1 || (kind != "backups" && kind != "snapshots" && kind != "backup-schedules") {
		return "", fmt.Errorf("invalid VM protection address")
	}
	p := fmt.Sprintf("/api/v2/vms/%d/%s", vmID, kind)
	if id > 0 {
		p += fmt.Sprintf("/%d", id)
	}
	return p, nil
}
func (c *Client) GetVmProtection(ctx context.Context, vmID int64, kind string, id int64, opts *RequestOpts) (*VmProtection, error) {
	if id < 1 {
		return nil, fmt.Errorf("invalid VM protection object ID")
	}
	p, err := protectionPath(vmID, kind, id)
	if err != nil {
		return nil, err
	}
	var out VmProtection
	if err = c.Do(ctx, http.MethodGet, p, nil, &out, opts); err != nil {
		return nil, err
	}
	if out.Contract != vmProtectionContract || out.ID != id || out.VmID != vmID {
		return nil, fmt.Errorf("API did not confirm VM protection identity; state is retained")
	}
	return &out, nil
}
func (c *Client) SaveVmProtection(ctx context.Context, vmID int64, kind string, id int64, req VmProtectionRequest, opts *RequestOpts) (*VmProtection, error) {
	p, err := protectionPath(vmID, kind, id)
	if err != nil {
		return nil, err
	}
	method := http.MethodPost
	if id == 0 {
		if err := c.CheckVmProtectionCapabilities(ctx, opts); err != nil {
			return nil, err
		}
	}
	if id > 0 {
		method = http.MethodPut
	}
	var out VmProtection
	if err = c.Do(ctx, method, p, req, &out, opts); err != nil {
		return nil, err
	}
	if out.Contract != vmProtectionContract || !out.Present || out.ID < 1 || out.VmID != vmID || (id > 0 && out.ID != id) {
		return nil, fmt.Errorf("API did not confirm saved VM protection identity")
	}
	return &out, nil
}

// SubmitVmProtection consumes a known reservation. Its canonical ID is already in state.
func (c *Client) SubmitVmProtection(ctx context.Context, vmID int64, kind string, id int64, opts *RequestOpts) (*VmProtection, error) {
	p, err := protectionPath(vmID, kind, id)
	if err != nil {
		return nil, err
	}
	var out VmProtection
	if err = c.Do(ctx, http.MethodPost, p+"/submit", nil, &out, opts); err != nil {
		return nil, err
	}
	if out.Contract != vmProtectionContract || out.ID != id || out.VmID != vmID || !out.Present {
		return nil, fmt.Errorf("API did not confirm submitted VM protection identity")
	}
	return &out, nil
}

func (c *Client) DeleteVmProtection(ctx context.Context, vmID int64, kind string, id int64, opts *RequestOpts) error {
	p, err := protectionPath(vmID, kind, id)
	if err != nil {
		return err
	}
	return c.Do(ctx, http.MethodDelete, p, nil, nil, opts)
}

type VmBackupRestoreRequest struct {
	Name        string `json:"name"`
	CPUCores    int64  `json:"cpuCores"`
	RAM         int64  `json:"ram"`
	Description string `json:"description,omitempty"`
}
type VmBackupRestoreResponse struct {
	VmID     int64  `json:"vmId"`
	TaskUUID string `json:"taskUuid"`
	Password string `json:"password"`
}

func (c *Client) RestoreVmBackup(ctx context.Context, id int64, req VmBackupRestoreRequest, opts *RequestOpts) (*VmBackupRestoreResponse, error) {
	if err := c.CheckVmProtectionCapabilities(ctx, opts); err != nil {
		return nil, err
	}
	var out VmBackupRestoreResponse
	if err := c.Do(ctx, http.MethodPost, fmt.Sprintf("/api/v2/vm-backups/%d/restore-as-new-vm/reservations", id), req, &out, opts); err != nil {
		return nil, err
	}
	if out.VmID < 1 {
		return nil, fmt.Errorf("API did not return a durable restored VM identity")
	}
	return &out, nil
}

// UpdateVmDescription changes only panel metadata and never stops the guest.
func (c *Client) UpdateVmDescription(ctx context.Context, id int64, description string, opts *RequestOpts) error {
	return c.Do(ctx, http.MethodPatch, fmt.Sprintf("/api/v2/vms/%d/description", id), struct {
		Description string `json:"description"`
	}{description}, nil, opts)
}

type VmBackupRestoreOperation struct {
	Contract string `json:"contract"`
	VmID     int64  `json:"vmId"`
	Status   string `json:"status"`
	Password string `json:"password"`
}

func (c *Client) vmBackupRestoreOperation(ctx context.Context, method string, backupID, vmID int64, suffix string, opts *RequestOpts) (*VmBackupRestoreOperation, error) {
	var out VmBackupRestoreOperation
	if err := c.Do(ctx, method, fmt.Sprintf("/api/v2/vm-backups/%d/restores/%d%s", backupID, vmID, suffix), nil, &out, opts); err != nil {
		return nil, err
	}
	if out.Contract != "vm-protection-reservation-v1" || out.VmID != vmID || out.Status == "" {
		return nil, fmt.Errorf("unverified restore operation identity")
	}
	return &out, nil
}
func (c *Client) SubmitVmBackupRestore(ctx context.Context, backupID, vmID int64, opts *RequestOpts) (*VmBackupRestoreOperation, error) {
	return c.vmBackupRestoreOperation(ctx, http.MethodPost, backupID, vmID, "/submit", opts)
}
func (c *Client) GetVmBackupRestore(ctx context.Context, backupID, vmID int64, opts *RequestOpts) (*VmBackupRestoreOperation, error) {
	return c.vmBackupRestoreOperation(ctx, http.MethodGet, backupID, vmID, "", opts)
}
func (c *Client) CancelVmBackupRestoreReservation(ctx context.Context, backupID, vmID int64, opts *RequestOpts) (bool, error) {
	var cancelled bool
	err := c.Do(ctx, http.MethodDelete, fmt.Sprintf("/api/v2/vm-backups/%d/restores/%d/reservation", backupID, vmID), nil, &cancelled, opts)
	return cancelled, err
}
