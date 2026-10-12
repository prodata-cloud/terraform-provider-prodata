package resources

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

const (
	// vmRestartPollInterval is how often restartRunningVM reads the VM's status.
	vmRestartPollInterval = 5 * time.Second

	// vmRestartMaxPollErrs is how many status reads in a row may fail before restartRunningVM
	// gives up on a VM it has not stopped: one bad read is a blip, not a verdict. Once a stop of
	// ours may have taken effect the VM may well be off, and giving up would leave it so, so a read
	// that fails in a way that may pass (see inconclusive) gets the patience of a refused call
	// instead.
	vmRestartMaxPollErrs = 3

	// vmRestartRefusalPatience is how long, at the default poll interval, a stop or a start may
	// keep failing in a way that does not say whether the call was carried out (see inconclusive)
	// while the VM stays in the status it was refused in. The panel answers 627 both when the VM is
	// not in a status that allows the call — a race with another operation, which the next read
	// resolves — and for any failure of the hypervisor call behind it, which no amount of waiting
	// fixes. It answers 627, and the gateway in front of it a 5xx, for calls that were carried out
	// all the same (the read timeout of a hop behind it, an exception after the hypervisor call),
	// which is why such a failure is judged by the status that follows it, not taken at its word.
	// And a refusal takes the panel's own status write back with it, so the VM goes on reading as
	// it did before the call until the panel next mirrors the hypervisor. It does so in two passes,
	// each every 20 seconds or so, and a pass works from what the one before it left, so the lag
	// can reach over a minute: the patience has to outlast that, or a stop or a start that
	// went through would be reported as refused.
	vmRestartRefusalPatience = 3 * time.Minute

	// vmRestartMaxRefusals is that patience as a number of calls: the first, and one more after
	// each poll interval.
	vmRestartMaxRefusals = int(vmRestartRefusalPatience/vmRestartPollInterval) + 1

	// vmRestartMaxStoppedPolls is how many status reads in a row may show STOPPED after the
	// panel accepted the start, before restartRunningVM concludes that the start did not take
	// hold. It is the patience of a refused call, and for the same reason. The panel normally
	// holds a VM it has just been told to start in STARTING for two minutes whatever the
	// hypervisor reports, but not every VM has that protection, and where it is missing the
	// first pass of the panel's mirror to run after the start works from a snapshot that still
	// has the VM stopped and writes that, until a pass with a fresh snapshot corrects it: a lag
	// of about a minute, and a start handed to the hypervisor's HA manager takes tens of
	// seconds more to show. Concluding sooner would call a start that went through failed,
	// taint the attachment, and have the next apply stop a VM that is up. Waiting costs time
	// only where the start really has failed, and then it only brings forward an error that the
	// deadline would give anyway.
	vmRestartMaxStoppedPolls = vmRestartMaxRefusals
)

type vmRestartPhase int

const (
	restartLooking  vmRestartPhase = iota // deciding from the VM's status whether to stop it
	restartStopping                       // a stop took effect or was accepted; waiting for STOPPED
	restartStarting                       // a start was accepted or seen; waiting for RUNNING
)

// inconclusive reports whether err says nothing final about the request it came from: the panel's
// catch-all 627; a 5xx, such as the gateway in front of the panel timing out or restarting; a rate
// limit that outlasted the client's own backoff; or a request that failed on its way, a dropped
// connection, which is not an answer from the panel at all. A stop or a start that fails like this
// may or may not have been carried out, and a read that fails like this may well succeed next time.
// Any other answer (403, 404, ...) is the panel's last word.
func inconclusive(err error) bool {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return true
	}
	return apiErr.HasCode(627) || apiErr.StatusCode >= http.StatusInternalServerError || apiErr.StatusCode == http.StatusTooManyRequests
}

// restartRunningVM power-cycles a VM so that the network configuration the panel wrote for a
// newly attached public IP takes effect, and reports whether it did. The deadline is carried
// by ctx; poll is the interval between status reads.
//
// Only a VM that is up is cycled. One that is stopped, or on its way to being stopped, is left
// as it is: the panel builds the configuration when the VM next starts, and starting a VM that
// somebody wanted off is not this function's call. One that is starting or rebooting is waited
// for first, because it may have read its configuration before the attach.
//
// The panel's v2 API has no restart, and its stop is not a clean shutdown of the guest (the
// hypervisor's "stop", a power-cut; for a VM under the hypervisor's HA manager, an HA stop
// request), so the cycle is: stop, wait for STOPPED, start, wait for RUNNING. RUNNING is the
// panel's view of the VM; the guest operating system may still be booting.
//
// A failed call is judged by what the VM does next, because the failure does not always say
// whether the call was carried out: the panel's 627 does not, and nor does a gateway that drops
// the reply of a call the panel carried out. A stop that failed this way, followed by STOPPING or
// STOPPED, is carried on as a stop that took effect, and a VM seen STOPPED and then RUNNING has
// been cycled whoever started it. From the moment a stop of ours may have taken effect the VM may
// be off because of it, so until it is running again a failure that may pass (a 5xx, a dropped
// connection, a refusal) is waited out for vmRestartRefusalPatience instead of being given up on.
func restartRunningVM(ctx context.Context, c *client.Client, vmID int64, opts *client.RequestOpts, poll time.Duration) (bool, error) {
	var (
		phase       = restartLooking
		pollErrs    int
		lastPollErr error
		refusals    int
		status      = "unknown"

		stopRefused  bool // a stop of ours failed inconclusively, and the VM has shown no reason for that since
		sawStopped   bool // the VM has been STOPPED since our stop took effect
		stoppedPolls int  // consecutive reads of STOPPED since the panel accepted our start
	)

	// callFailed deals with a stop or a start that failed and returns the error to give up with,
	// or nil to go on and let the VM's next status say whether the call was carried out. A failure
	// that is inconclusive is gone on from until the patience is spent; any other is final, the
	// panel's own word that it will not do what was asked. A call cut short by ctx fails
	// inconclusively too, and the wait below, which comes next, says whether the restart was
	// interrupted or ran out of time.
	callFailed := func(what, giveUp string, err error) error {
		if !inconclusive(err) {
			return fmt.Errorf("%s: %w", giveUp, err)
		}
		refusals++
		tflog.Warn(ctx, "VM power operation failed; judging it by the VM's status", map[string]any{
			"vm_id": vmID, "operation": what, "status": status, "refusals": refusals, "error": err.Error(),
		})
		if refusals >= vmRestartMaxRefusals {
			return fmt.Errorf("the panel keeps refusing to %s VM %d (status %s): %w", what, vmID, status, err)
		}
		return nil
	}

	for {
		vm, err := c.GetVmStatus(ctx, vmID, opts)
		switch {
		case err != nil && ctx.Err() == nil:
			pollErrs++
			lastPollErr = err
			tflog.Warn(ctx, "Transient error polling VM status", map[string]any{
				"vm_id": vmID, "error": err.Error(), "consecutive_errors": pollErrs,
			})
			limit := vmRestartMaxPollErrs
			if (phase != restartLooking || stopRefused) && inconclusive(err) {
				limit = vmRestartMaxRefusals
			}
			if pollErrs >= limit {
				return false, fmt.Errorf("reading the status of VM %d: %w (after %d consecutive failures)", vmID, err, pollErrs)
			}

		case err == nil:
			pollErrs = 0
			status = vm.Status

			switch phase {
			case restartLooking:
				switch status {
				case "RUNNING":
					tflog.Info(ctx, "Stopping VM so the public IP takes effect", map[string]any{"vm_id": vmID})
					if err := c.StopVm(ctx, vmID, opts); err != nil {
						if err := callFailed("stop", fmt.Sprintf("stopping VM %d", vmID), err); err != nil {
							return false, err
						}
						stopRefused = true
					} else {
						phase, refusals, stopRefused = restartStopping, 0, false
					}
				case "STOPPING":
					if stopRefused {
						// Refused, and yet the VM is stopping: the stop took effect after all.
						phase, refusals = restartStopping, 0
					}
				case "STOPPED":
					if !stopRefused {
						return false, nil
					}
					// Refused, and yet the VM is stopped: the stop took effect after all. The
					// next read finds it STOPPED in the stopping phase and starts it.
					phase, refusals, sawStopped = restartStopping, 0, true
				case "STARTING", "REBOOTING", "CREATING":
					// A transitional status explains a refused stop; the next read tells more.
					stopRefused, refusals = false, 0
				default:
					return false, fmt.Errorf("VM %d is in status %s and cannot be restarted", vmID, status)
				}

			case restartStopping:
				switch status {
				case "STOPPED":
					sawStopped = true
					tflog.Info(ctx, "Starting VM so the public IP takes effect", map[string]any{"vm_id": vmID})
					if err := c.StartVm(ctx, vmID, opts); err != nil {
						if err := callFailed("start", fmt.Sprintf("starting VM %d after stopping it", vmID), err); err != nil {
							return false, err
						}
					} else {
						phase = restartStarting
					}
				case "STARTING", "REBOOTING":
					// Somebody else started it again, or a start we were told was refused went
					// through: it is coming up with the new configuration.
					phase = restartStarting
				case "RUNNING":
					if sawStopped {
						// Stopped, and running again before we saw it start: the start we were told
						// was refused went through, or somebody else started the VM.
						return true, nil
					}
					// The stop is still taking hold, or the panel's view of the VM lags: the panel
					// reconciles with the hypervisor on a timer, so a VM it was stopping can read
					// RUNNING again for a read or two before it reads STOPPED. That is no reason to
					// give up, and a guard against it would fire on a healthy stop. A VM under the
					// hypervisor's HA manager is shut down with a timeout before it is powered off,
					// so a stop can also take minutes. The deadline bounds the wait.
				case "ERROR", "DELETED":
					return false, fmt.Errorf("VM %d went to status %s after it was stopped", vmID, status)
				default:
					// STOPPING: the stop is still taking hold, and a refused start before it is
					// forgotten with the status it was refused in.
					refusals = 0
				}

			case restartStarting:
				switch status {
				case "RUNNING":
					return true, nil
				case "ERROR", "DELETED":
					return false, fmt.Errorf("VM %d went to status %s while it was starting", vmID, status)
				case "STOPPED":
					stoppedPolls++
					if stoppedPolls >= vmRestartMaxStoppedPolls {
						return false, fmt.Errorf("VM %d is STOPPED again after it was seen starting: the start did not take hold", vmID)
					}
				default:
					stoppedPolls = 0
				}
			}
		}

		select {
		case <-ctx.Done():
			outcome := "timed out"
			if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				outcome = "interrupted"
			}
			unread := ""
			if pollErrs > 0 {
				unread = fmt.Sprintf("; the last %d status reads failed, the latest with: %v", pollErrs, lastPollErr)
			}
			return false, fmt.Errorf("%s %s (last status %s%s): %w", outcome, vmRestartWaitingFor(phase, sawStopped, vmID), status, unread, ctx.Err())
		case <-time.After(poll):
		}
	}
}

func vmRestartWaitingFor(phase vmRestartPhase, sawStopped bool, vmID int64) string {
	switch {
	case phase == restartStarting, phase == restartStopping && sawStopped:
		return fmt.Sprintf("waiting for VM %d to start", vmID)
	case phase == restartStopping:
		return fmt.Sprintf("waiting for VM %d to stop", vmID)
	default:
		return fmt.Sprintf("waiting for VM %d to settle", vmID)
	}
}
