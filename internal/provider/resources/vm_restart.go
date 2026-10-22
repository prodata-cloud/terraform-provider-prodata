package resources

import (
	"context"
	"errors"
	"fmt"
	"math"
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

	// vmStartReserve is the time a VM that a cycle with a hook stopped keeps for being started
	// again, out of the time the cycle was given: the start itself (five minutes, the time the
	// provider allows a start elsewhere) and the patience of a refused one. The hook gets the time
	// before it; the start after it runs on a context of its own that is good for this long however
	// the hook ended, so that a deadline or an interruption in the middle of the hook cannot leave a
	// VM off that the provider turned off.
	vmStartReserve = 5*time.Minute + vmRestartRefusalPatience

	// vmHookMinBudget is the least time a hook is given. A cycle that finds less than this left
	// when the VM is stopped does not run it: what a hook does to a stopped VM is not worth
	// starting if there is no time to finish it, and the VM is started again at once.
	vmHookMinBudget = 5 * time.Minute
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

// errHookTimeShort is returned, wrapped, by a cycle that has too little time left for its hook: less
// than vmHookMinBudget once the time kept for starting the VM again is set aside. The outcome says
// whether the VM was stopped by then (hookSkipped) or not.
var errHookTimeShort = errors.New("not enough time is left for the work on the stopped VM")

// restartOutcome is what restartRunningVMWith reports besides its error.
type restartOutcome struct {
	// cycled is true when the VM was stopped by the cycle, or was seen stopped after a stop of
	// ours, and was then seen RUNNING again.
	cycled bool
	// hookRan is true when the hook was called, whatever it returned; hookErr is what it returned.
	hookRan bool
	hookErr error
	// hookSkipped is true when the VM was stopped but less than vmHookMinBudget was left for the
	// hook, so it was not called and the VM was started again.
	hookSkipped bool
}

// restartRunningVM power-cycles a VM so that the network configuration the panel wrote for a
// newly attached interface or public IP takes effect, and reports whether it did. The deadline is
// carried by ctx; poll is the interval between status reads.
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
	out, err := restartRunningVMWith(ctx, c, vmID, opts, poll, nil)
	return out.cycled, err
}

// restartRunningVMWith is restartRunningVM with a hook: whileStopped is called, at most once,
// when the VM that this cycle stopped is seen STOPPED and before it is started again, and the VM
// is started again however the hook ends. A VM that was stopped to begin with is not cycled and
// the hook is not called: the caller does its work on it directly (cycled and hookRan are both
// false and the error is nil), and one that somebody else started in the middle of the cycle is
// not given to the hook either (cycled is true and hookRan is false).
//
// The hook and the start after it have budgets of their own, so that the work of the hook cannot
// eat the time the VM needs to come up again. The hook gets the context ctx carries less
// vmStartReserve; the start, and the wait for RUNNING after it, run on a context that neither a
// deadline nor an interruption of ctx can cancel (see reserveContext). A hook that is left less
// than vmHookMinBudget is not called at all (hookSkipped), and a cycle that would stop the VM with
// that little left does not stop it (errHookTimeShort, nothing was done). That check is made before
// the first stop only: once a stop of ours was refused it may have been carried out, and the cycle
// goes on until the VM shows what became of it. However the cycle ends before the hook has run (the
// context, the patience, status reads that fail), a VM that a stop of ours stopped or may have
// stopped is started again, and the error says what became of it.
//
// Without a hook (nil) the function is restartRunningVM, contexts and timeouts included.
func restartRunningVMWith(ctx context.Context, c *client.Client, vmID int64, opts *client.RequestOpts, poll time.Duration, whileStopped func(context.Context) error) (restartOutcome, error) {
	var (
		out         restartOutcome
		phase       = restartLooking
		pollErrs    int
		lastPollErr error
		refusals    int
		status      = "unknown"

		stopRefused  bool // a stop of ours failed inconclusively, and the VM has shown no reason for that since
		sawStopped   bool // the VM has been STOPPED since our stop took effect
		stoppedPolls int  // consecutive reads of STOPPED since the panel accepted our start

		hookDone bool // the hook ran, or was passed over for lack of time: it is not called again, and ctx is the context of the reserve
	)
	parent := ctx

	// giveUp ends the cycle with err. The hook is the reason the VM is stopped, and while it has not
	// run, a stop of ours that took effect, was accepted or failed in a way that does not say has
	// left the VM off, or may have, because of us: it is started again before the cycle returns,
	// whatever ended it, and err says what became of it. A VM that reads RUNNING is looked at for
	// longer (doubtful, see startAgain) while the panel's view of it may be behind the hypervisor's:
	// after a stop that was refused (see stopRefused), and after one that was accepted for as long as
	// the VM has not been seen STOPPED. Not when the refusals or the failed reads have outlasted the
	// patience already (waited): a lag has passed by then.
	giveUp := func(err error, waited bool) (restartOutcome, error) {
		if whileStopped != nil && !hookDone && (phase == restartStopping || phase == restartLooking && stopRefused) {
			doubtful := !waited && (phase == restartStopping && !sawStopped || phase == restartLooking && stopRefused)
			err = fmt.Errorf("%w; %s", err, startAfterInterruption(parent, c, vmID, opts, poll, doubtful))
		}
		return out, err
	}

	// callFailed deals with a stop or a start that failed and returns the error to give up with,
	// or nil to go on and let the VM's next status say whether the call was carried out. A failure
	// that is inconclusive is gone on from until the patience is spent; any other is final, the
	// panel's own word that it will not do what was asked. A call cut short by ctx fails
	// inconclusively too, and the wait below, which comes next, says whether the restart was
	// interrupted or ran out of time.
	callFailed := func(what, finalText string, err error) error {
		if !inconclusive(err) {
			return fmt.Errorf("%s: %w", finalText, err)
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
				// Waited only when the reads have failed for the whole patience: a few reads that fail
				// for good say nothing of how far behind the panel's view of the VM is.
				return giveUp(fmt.Errorf("reading the status of VM %d: %w (after %d consecutive failures)", vmID, err, pollErrs), limit == vmRestartMaxRefusals)
			}

		case err == nil:
			pollErrs = 0
			status = vm.Status

			switch phase {
			case restartLooking:
				switch status {
				case "RUNNING":
					// Not once a stop of ours has been refused: it may have been carried out all the same,
					// and "was not stopped" would then be untrue. The stop is repeated, and the VM that
					// shows STOPPED is started again at once if the hook has no time left (hookSkipped).
					if left := hookBudget(ctx); whileStopped != nil && !stopRefused && left < vmHookMinBudget {
						return out, fmt.Errorf("%w: %s are left, less than the %s the work needs once the VM is stopped, so VM %d was not stopped",
							errHookTimeShort, left.Round(time.Second), vmHookMinBudget, vmID)
					}
					tflog.Info(ctx, "Stopping VM to start it again", map[string]any{"vm_id": vmID})
					if err := c.StopVm(ctx, vmID, opts); err != nil {
						if err := callFailed("stop", fmt.Sprintf("stopping VM %d", vmID), err); err != nil {
							// An answer that is final after a refusal that may have been carried out is no
							// patience spent: the VM is still looked at for as long as a lag can last.
							return giveUp(err, refusals >= vmRestartMaxRefusals)
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
						return out, nil
					}
					// Refused, and yet the VM is stopped: the stop took effect after all. The
					// next read finds it STOPPED in the stopping phase and starts it.
					phase, refusals, sawStopped = restartStopping, 0, true
				case "STARTING", "REBOOTING", "CREATING":
					// A transitional status explains a refused stop; the next read tells more.
					stopRefused, refusals = false, 0
				default:
					return out, fmt.Errorf("VM %d is in status %s and cannot be restarted", vmID, status)
				}

			case restartStopping:
				switch status {
				case "STOPPED":
					sawStopped = true
					if whileStopped != nil && !hookDone {
						hookDone = true
						if hookBudget(ctx) < vmHookMinBudget {
							out.hookSkipped = true
						} else {
							hookCtx, cancelHook := hookContext(ctx)
							out.hookRan = true
							out.hookErr = whileStopped(hookCtx)
							cancelHook()
						}
						var cancelReserve context.CancelFunc
						ctx, cancelReserve = reserveContext(parent)
						defer cancelReserve()
					}
					tflog.Info(ctx, "Starting VM again", map[string]any{"vm_id": vmID})
					if err := c.StartVm(ctx, vmID, opts); err != nil {
						if err := callFailed("start", fmt.Sprintf("starting VM %d after stopping it", vmID), err); err != nil {
							return out, err
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
						out.cycled = true
						return out, nil
					}
					// The stop is still taking hold, or the panel's view of the VM lags: the panel
					// reconciles with the hypervisor on a timer, so a VM it was stopping can read
					// RUNNING again for a read or two before it reads STOPPED. That is no reason to
					// give up, and a guard against it would fire on a healthy stop. A VM under the
					// hypervisor's HA manager is shut down with a timeout before it is powered off,
					// so a stop can also take minutes. The deadline bounds the wait.
				case "ERROR", "DELETED":
					return out, fmt.Errorf("VM %d went to status %s after it was stopped", vmID, status)
				default:
					// STOPPING: the stop is still taking hold, and a refused start before it is
					// forgotten with the status it was refused in.
					refusals = 0
				}

			case restartStarting:
				switch status {
				case "RUNNING":
					out.cycled = true
					return out, nil
				case "ERROR", "DELETED":
					return out, fmt.Errorf("VM %d went to status %s while it was starting", vmID, status)
				case "STOPPED":
					stoppedPolls++
					if stoppedPolls >= vmRestartMaxStoppedPolls {
						return out, fmt.Errorf("VM %d is STOPPED again after it was seen starting: the start did not take hold", vmID)
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
			// If a stop of ours has taken, or may have taken, effect and the hook has not run, the VM is
			// off because of us, or may be: giveUp starts it again.
			return giveUp(fmt.Errorf("%s %s (last status %s%s): %w", outcome, vmRestartWaitingFor(phase, sawStopped, vmID), status, unread, ctx.Err()), false)
		case <-time.After(poll):
		}
	}
}

// hookBudget is the time a hook would be given if the VM were stopped now: what ctx has left,
// less the time kept for starting the VM again. A context without a deadline has all the time
// there is.
func hookBudget(ctx context.Context) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return time.Duration(math.MaxInt64)
	}
	return time.Until(deadline) - vmStartReserve
}

// hookContext is the context of a hook: ctx, with the time kept for starting the VM again taken
// off its deadline.
func hookContext(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, deadline.Add(-vmStartReserve))
}

// reserveContext is the context the VM that a cycle stopped is started on, and waited for on: it
// outlives the cancellation and the deadline of ctx, so that a timeout or an interruption in the
// middle of the hook leaves no VM off that the provider turned off. It is good for as long as the
// deadline of ctx allows, and for no less than vmStartReserve.
func reserveContext(ctx context.Context) (context.Context, context.CancelFunc) {
	budget := vmStartReserve
	if deadline, ok := ctx.Deadline(); ok {
		if left := time.Until(deadline); left > budget {
			budget = left
		}
	}
	return context.WithTimeout(context.WithoutCancel(ctx), budget)
}

// startAfterInterruption starts a VM that a cycle of ours stopped, or may have stopped, when the
// cycle ended before the hook could run: the work of the hook has not been done, and the VM is to
// be as it was. It runs on a context of its own (see reserveContext) and returns what is to be
// added to the error that ends the cycle. doubtful is true when a stop of ours may have been
// carried out without the VM showing it yet: the stop was refused in a way that does not say, or was
// accepted and the VM has not been seen stopped since (see startAgain).
func startAfterInterruption(ctx context.Context, c *client.Client, vmID int64, opts *client.RequestOpts, poll time.Duration, doubtful bool) string {
	ctx, cancel := reserveContext(ctx)
	defer cancel()

	up, said := startAgain(ctx, c, vmID, opts, poll, doubtful)
	if up {
		return said + ", nothing was changed"
	}
	return said
}

// startAgain makes sure that a VM which was stopped, or may have been, is up again, and says what
// became of it. ctx is one that the VM can still be started on (see reserveContext). A start that
// fails, and a read of the status that fails, are repeated for as long as the restart cycle repeats
// a refused call: the platform may still be busy with the call the VM was stopped for.
//
// A VM that reads RUNNING is up, unless doubtful says that a stop of ours may have been carried out
// although the panel's view of the VM does not show it yet. A stop that the panel refused takes its
// own status write back with it; one that it accepted can be overwritten with RUNNING by the next
// pass of its mirror for a read or two, and under the hypervisor's HA manager the VM is shut down
// with a timeout first, which takes minutes. Either way the VM goes on reading RUNNING until the
// panel next mirrors the hypervisor (see vmRestartRefusalPatience): such a VM is looked at for as
// long as a lag can last, and is started if it turns out to be off.
func startAgain(ctx context.Context, c *client.Client, vmID int64, opts *client.RequestOpts, poll time.Duration, doubtful bool) (up bool, said string) {
	status, failures, running := "unknown", 0, 0
	for {
		vm, err := c.GetVmStatus(ctx, vmID, opts)
		if err == nil {
			status = vm.Status
			if status == "RUNNING" {
				running++
			} else {
				running = 0
			}
			switch status {
			case "RUNNING":
				if !doubtful || running >= vmRestartMaxRefusals {
					return true, fmt.Sprintf("VM %d is up", vmID)
				}
			case "STARTING", "REBOOTING":
				return true, fmt.Sprintf("VM %d is up", vmID)
			case "STOPPED":
				if err = c.StartVm(ctx, vmID, opts); err == nil {
					return true, fmt.Sprintf("VM %d was started again", vmID)
				}
			case "STOPPING":
				// The stop is still taking hold; the next read tells more.
			default:
				return false, fmt.Sprintf("VM %d is in status %s and was not started again", vmID, status)
			}
		}
		if err != nil {
			if failures++; failures >= vmRestartMaxRefusals {
				return false, fmt.Sprintf("VM %d was not started again: %v", vmID, err)
			}
		}
		select {
		case <-ctx.Done():
			return false, fmt.Sprintf("VM %d was not started again before the time ran out (last status %s)", vmID, status)
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
