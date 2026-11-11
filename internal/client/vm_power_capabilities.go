package client

import (
	"context"
	"fmt"
	"net/http"
	"slices"
)

const VmPowerOperationContract = "vm-power-operation-v1"

// RequireVmPowerOperations checks the power protocol independently of guest OS/network support.
// Existing operation reads and cleanup must remain available when new admission is disabled.
func (c *Client) RequireVmPowerOperations(ctx context.Context, vmID int64, opts *RequestOpts) error {
	if vmID < 1 {
		return fmt.Errorf("invalid VM identity for power operation")
	}
	var caps struct {
		Contract                 string   `json:"contract"`
		CallerUUID               bool     `json:"callerUuid"`
		ClaimBeforeWrite         bool     `json:"claimBeforeWrite"`
		ReadNeverActivates       bool     `json:"readNeverActivates"`
		ResolveTombstone         bool     `json:"resolveTombstone"`
		RecoverablePowerChildren bool     `json:"recoverablePowerChildren"`
		StopCheckpoint           bool     `json:"stopCheckpoint"`
		SupportedActions         []string `json:"supportedActions"`
	}
	path := scopedPath(fmt.Sprintf("/api/v2/vms/%d/power-operations/capabilities", vmID), opts)
	if err := c.Do(ctx, http.MethodGet, path, nil, &caps, opts); err != nil {
		return err
	}
	if caps.Contract != VmPowerOperationContract || !caps.CallerUUID || !caps.ClaimBeforeWrite || !caps.ReadNeverActivates || !caps.ResolveTombstone || !caps.RecoverablePowerChildren || !caps.StopCheckpoint {
		return fmt.Errorf("the panel must support recoverable VM power operations before an automatic restart")
	}
	for _, action := range []string{"START", "REBOOT", "RESET", "RESUME", "STOP", "SHUTDOWN"} {
		if !slices.Contains(caps.SupportedActions, action) {
			return fmt.Errorf("the panel did not confirm recoverable %s power operations", action)
		}
	}
	return nil
}
