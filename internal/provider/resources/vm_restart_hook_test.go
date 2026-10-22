package resources

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"terraform-provider-prodata/internal/client"
)

// hookRecorder is a hook for restartRunningVMWith that notes when it was called and what the VM
// and the panel looked like then.
type hookRecorder struct {
	p *vmPanel

	calls     int
	statusAt  string
	callsAt   []string
	deadline  time.Time
	hasLimit  bool
	err       error
	beforeEnd func(ctx context.Context)
}

func (h *hookRecorder) hook(ctx context.Context) error {
	h.calls++
	h.statusAt = h.p.current()
	h.callsAt = h.p.posts()
	h.deadline, h.hasLimit = ctx.Deadline()
	if h.beforeEnd != nil {
		h.beforeEnd(ctx)
	}
	return h.err
}

// restartWithHook runs restartRunningVMWith with the hook against the panel, under a deadline of d.
func (p *vmPanel) restartWithHook(t *testing.T, c *client.Client, d time.Duration, h *hookRecorder) (restartOutcome, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return restartRunningVMWith(ctx, c, vmPanelVMID, &client.RequestOpts{}, restartTestPoll, h.hook)
}

const generousTime = 20 * time.Minute

func TestRestartRunningVMWith_TheHookRunsOnceWhileTheVMIsStopped(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	h := &hookRecorder{p: p}

	out, err := p.restartWithHook(t, c, generousTime, h)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.cycled || !out.hookRan || out.hookErr != nil || out.hookSkipped {
		t.Errorf("outcome = %+v, want cycled and hookRan, nothing else", out)
	}
	if h.calls != 1 {
		t.Fatalf("hook called %d times, want once", h.calls)
	}
	if h.statusAt != "STOPPED" {
		t.Errorf("the VM was %s when the hook ran, want STOPPED", h.statusAt)
	}
	wantCalls(t, h.callsAt, []string{"stop"})
	wantCalls(t, p.posts(), []string{"stop", "start"})
	if got := p.current(); got != "RUNNING" {
		t.Errorf("the VM is %s at the end, want RUNNING", got)
	}
}

func TestRestartRunningVMWith_TheHookGetsTheTimeLessTheReserve(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	h := &hookRecorder{p: p}
	ctx, cancel := context.WithTimeout(context.Background(), generousTime)
	defer cancel()
	deadline, _ := ctx.Deadline()

	if _, err := restartRunningVMWith(ctx, c, vmPanelVMID, &client.RequestOpts{}, restartTestPoll, h.hook); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !h.hasLimit {
		t.Fatal("the hook has no deadline: it could eat the time the start of the VM needs")
	}
	if want := deadline.Add(-vmStartReserve); h.deadline.Sub(want) > time.Millisecond || want.Sub(h.deadline) > time.Millisecond {
		t.Errorf("hook deadline = %v, want %v: the deadline of the cycle less the %v kept for the start", h.deadline, want, vmStartReserve)
	}
}

func TestRestartRunningVMWith_TheReserveIsTheStartAndTheRefusalPatience(t *testing.T) {
	if want := 5*time.Minute + vmRestartRefusalPatience; vmStartReserve != want {
		t.Errorf("vmStartReserve = %v, want %v", vmStartReserve, want)
	}
	if vmStartReserve != 8*time.Minute {
		t.Errorf("vmStartReserve = %v, the documentation says 8m", vmStartReserve)
	}
	if vmHookMinBudget != 5*time.Minute {
		t.Errorf("vmHookMinBudget = %v, the documentation says 5m", vmHookMinBudget)
	}
}

func TestRestartRunningVMWith_AFailedHookStillStartsTheVM(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	failure := errors.New("the move failed")
	h := &hookRecorder{p: p, err: failure}

	out, err := p.restartWithHook(t, c, generousTime, h)

	if err != nil {
		t.Fatalf("unexpected error of the cycle: %v: the failure of the hook is not the cycle's", err)
	}
	if !out.hookRan || !errors.Is(out.hookErr, failure) {
		t.Errorf("outcome = %+v, want the error of the hook", out)
	}
	if !out.cycled {
		t.Errorf("outcome = %+v, want the VM running again", out)
	}
	wantCalls(t, p.posts(), []string{"stop", "start"})
	if got := p.current(); got != "RUNNING" {
		t.Errorf("the VM is %s at the end, want RUNNING", got)
	}
}

func TestRestartRunningVMWith_AStoppedVMIsLeftToTheCaller(t *testing.T) {
	c, p := newVMPanel(t, "STOPPED")
	h := &hookRecorder{p: p}

	out, err := p.restartWithHook(t, c, generousTime, h)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != (restartOutcome{}) {
		t.Errorf("outcome = %+v, want nothing done: the caller does its work on a VM that was stopped to begin with", out)
	}
	if h.calls != 0 {
		t.Errorf("hook called %d times for a VM nobody stopped, want 0", h.calls)
	}
	wantCalls(t, p.posts(), nil)
	if got := p.current(); got != "STOPPED" {
		t.Errorf("the VM is %s, want it left STOPPED", got)
	}
}

func TestRestartRunningVMWith_AStartThatIsRefusedDoesNotCallTheHookAgain(t *testing.T) {
	// After a start fails in a way that says nothing, the cycle stays in the phase of the stop and
	// reads STOPPED again: without a flag the hook would run once more on every such read.
	c, p := newVMPanel(t, "RUNNING")
	p.refuseCalls("start", 3)
	h := &hookRecorder{p: p}

	out, err := p.restartWithHook(t, c, generousTime, h)

	if err != nil || !out.cycled {
		t.Fatalf("restartRunningVMWith = %+v, %v; want a cycled VM", out, err)
	}
	if h.calls != 1 {
		t.Errorf("hook called %d times, want once", h.calls)
	}
	wantCalls(t, p.posts(), []string{"stop", "start", "start", "start", "start"})
}

func TestRestartRunningVMWith_AVMThatSomebodyStartedIsNotGivenToTheHook(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	// The stop is accepted, and the VM goes on to STARTING without ever reading STOPPED.
	p.setSteps([]string{"STOPPING", "STARTING", "RUNNING"}, nil)
	h := &hookRecorder{p: p}

	out, err := p.restartWithHook(t, c, generousTime, h)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.cycled || out.hookRan || out.hookSkipped {
		t.Errorf("outcome = %+v, want cycled with no hook: the caller tells the user the VM was started in the middle", out)
	}
	if h.calls != 0 {
		t.Errorf("hook called %d times on a VM that was running, want 0", h.calls)
	}
	wantCalls(t, p.posts(), []string{"stop"})
}

func TestRestartRunningVMWith_NoTimeLeftMeansTheVMIsNotStopped(t *testing.T) {
	// 10 minutes are less than the 8 kept for the start and the 5 the hook needs.
	c, p := newVMPanel(t, "RUNNING")
	h := &hookRecorder{p: p}

	out, err := p.restartWithHook(t, c, 10*time.Minute, h)

	if !errors.Is(err, errHookTimeShort) {
		t.Fatalf("err = %v, want one that is errHookTimeShort", err)
	}
	if out != (restartOutcome{}) || h.calls != 0 {
		t.Errorf("outcome = %+v, hook called %d times, want nothing done", out, h.calls)
	}
	wantCalls(t, p.posts(), nil)
	if got := p.current(); got != "RUNNING" {
		t.Errorf("the VM is %s, want it left RUNNING: stopping it with no time to do anything is only harm", got)
	}
}

func TestRestartRunningVMWith_TheTimeThatJustSufficesStopsTheVM(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	h := &hookRecorder{p: p}

	out, err := p.restartWithHook(t, c, vmStartReserve+vmHookMinBudget+time.Minute, h)

	if err != nil || !out.hookRan || h.calls != 1 {
		t.Fatalf("restartRunningVMWith = %+v, %v; hook called %d times; want the hook run", out, err, h.calls)
	}
}

func TestRestartRunningVMWith_AStopThatTakesTheTimeLeavesTheHookOut(t *testing.T) {
	// At the stop there is a little more than the hook needs; the VM takes longer than that little
	// to read STOPPED. The VM is started again at once, and the hook is not called.
	c, p := newVMPanel(t, "RUNNING")
	p.setSteps(slices.Concat(slices.Repeat([]string{"STOPPING"}, 1000), []string{"STOPPED"}), nil)
	h := &hookRecorder{p: p}

	out, err := p.restartWithHook(t, c, vmStartReserve+vmHookMinBudget+300*time.Millisecond, h)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.hookSkipped || out.hookRan || !out.cycled {
		t.Errorf("outcome = %+v, want the hook skipped and the VM running again", out)
	}
	if h.calls != 0 {
		t.Errorf("hook called %d times with less than %v left, want 0", h.calls, vmHookMinBudget)
	}
	wantCalls(t, p.posts(), []string{"stop", "start"})
	if got := p.current(); got != "RUNNING" {
		t.Errorf("the VM is %s at the end, want RUNNING", got)
	}
}

func TestRestartRunningVMWith_TheStartIsNotCancelledWithTheContext(t *testing.T) {
	// The context of the cycle ends while the hook runs, as it does when the deadline passes or
	// the user interrupts. The VM that the cycle stopped is started all the same, and waited for.
	c, p := newVMPanel(t, "RUNNING")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &hookRecorder{p: p, err: context.Canceled, beforeEnd: func(context.Context) { cancel() }}

	out, err := restartRunningVMWith(ctx, c, vmPanelVMID, &client.RequestOpts{}, restartTestPoll, h.hook)

	if err != nil {
		t.Fatalf("unexpected error: %v: the VM must be started whatever became of the context", err)
	}
	if !out.cycled || !out.hookRan || !errors.Is(out.hookErr, context.Canceled) {
		t.Errorf("outcome = %+v, want the VM running again and the hook's cancellation reported", out)
	}
	wantCalls(t, p.posts(), []string{"stop", "start"})
	if got := p.current(); got != "RUNNING" {
		t.Errorf("the VM is %s at the end, want RUNNING", got)
	}
}

// cancelWhenStopped cancels ctx as soon as the panel has been sent the stop.
func cancelWhenStopped(p *vmPanel, cancel context.CancelFunc) {
	go func() {
		for !slices.Contains(p.posts(), "stop") {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
}

// cancelAtTheFirstReadAfterTheStop cancels ctx as the panel answers the first status read that follows
// the stop, so that the cycle has made the same number of reads whenever it is interrupted.
func cancelAtTheFirstReadAfterTheStop(p *vmPanel, cancel context.CancelFunc) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bodyExtra = func(map[string]any) {
		if slices.Contains(p.requests, "POST /api/v2/vms/7/stop") {
			cancel()
		}
	}
}

func TestRestartRunningVMWith_AnInterruptionBeforeSTOPPEDStartsTheVMAgain(t *testing.T) {
	// The stop was accepted and the context ends while the VM is still stopping. The hook has not
	// run, so nothing is to be done to the VM: it goes back to what it was.
	c, p := newVMPanel(t, "RUNNING")
	p.setSteps(slices.Concat(slices.Repeat([]string{"STOPPING"}, 100), []string{"STOPPED"}), nil)
	h := &hookRecorder{p: p}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelWhenStopped(p, cancel)

	out, err := restartRunningVMWith(ctx, c, vmPanelVMID, &client.RequestOpts{}, restartTestPoll, h.hook)

	if err == nil || !strings.Contains(err.Error(), "interrupted") || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the interruption", err)
	}
	if !strings.Contains(err.Error(), "was started again, nothing was changed") {
		t.Errorf("err = %v, want it to say the VM was started again", err)
	}
	if out.hookRan || h.calls != 0 || out.cycled {
		t.Errorf("outcome = %+v, hook called %d times, want the work not done", out, h.calls)
	}
	wantCalls(t, p.posts(), []string{"stop", "start"})
	if atStop, atStart := p.statuses(); atStop != "RUNNING" || atStart != "STOPPED" {
		t.Errorf("stopped at %q and started at %q; want the start only once the VM was STOPPED", atStop, atStart)
	}
}

func TestRestartRunningVMWith_AStopThatMayHaveTakenEffectIsUndoneToo(t *testing.T) {
	// The stop fails without saying whether it took effect, and the context ends before the next
	// read shows. The VM may be off because of us.
	c, p := newVMPanel(t, "RUNNING")
	p.loseNextReply("stop")
	p.setSteps(slices.Concat(slices.Repeat([]string{"STOPPING"}, 50), []string{"STOPPED"}), nil)
	h := &hookRecorder{p: p}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelWhenStopped(p, cancel)

	_, err := restartRunningVMWith(ctx, c, vmPanelVMID, &client.RequestOpts{}, restartTestPoll, h.hook)

	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the interruption", err)
	}
	if h.calls != 0 {
		t.Errorf("hook called %d times, want 0", h.calls)
	}
	if got := p.posts(); len(got) < 2 || got[len(got)-1] != "start" {
		t.Errorf("calls = %v, want the VM started again at the end", got)
	}
}

// A stop that the panel carried out but answered 627, on a panel whose view of the VM catches up late,
// and a budget that runs below what the hook needs while the VM still reads RUNNING: the stop is not
// taken for one that was never made. The cycle goes on until the VM shows what became of it, and the
// VM, stopped by then, is started at once because the hook has no time left.
func TestRestartRunningVMWith_ARefusedStopIsNotTakenForOneThatWasNotMade(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	// Fewer reads than the patience, but more time than the 20ms the budget has over the minimum.
	p.loseNextReplyShowingAfter("stop", vmRestartMaxRefusals-3)
	h := &hookRecorder{p: p}

	out, err := p.restartWithHook(t, c, vmStartReserve+vmHookMinBudget+20*time.Millisecond, h)

	if errors.Is(err, errHookTimeShort) {
		t.Fatalf("err = %v: a stop that was refused may have been carried out, and the VM was not stopped by this check", err)
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.hookSkipped || out.hookRan || !out.cycled || h.calls != 0 {
		t.Errorf("outcome = %+v, hook called %d times, want the hook skipped and the VM running again", out, h.calls)
	}
	posts := p.posts()
	if len(posts) < 3 || posts[0] != "stop" || posts[len(posts)-1] != "start" || slices.Contains(posts[:len(posts)-1], "start") {
		t.Errorf("calls = %v, want the stop repeated while it was refused and one start at the end", posts)
	}
	if got := p.current(); got != "RUNNING" {
		t.Errorf("the VM is %s at the end, want RUNNING", got)
	}
}

// The check of the budget is for a stop that was not made yet: with no stop of ours outstanding a
// budget that is short means the VM is left running.
func TestRestartRunningVMWith_TheBudgetIsStillCheckedBeforeTheFirstStop(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.failCalls("stop", 1, panelRefusal)
	h := &hookRecorder{p: p}

	// Less than the minimum from the very first read: the stop is never tried.
	_, err := p.restartWithHook(t, c, vmStartReserve+vmHookMinBudget-time.Second, h)

	if !errors.Is(err, errHookTimeShort) {
		t.Fatalf("err = %v, want one that is errHookTimeShort", err)
	}
	wantCalls(t, p.posts(), nil)
}

// Refusals that go on for longer than the patience, after a stop that was carried out: the cycle gives
// up on the stop, and the VM, which reads STOPPED by then, is started again. Before, the error was all
// there was, and the VM stayed off.
func TestRestartRunningVMWith_RefusalsThatOutlastThePatienceStartTheVMThatTheStopStopped(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	// The effect of the stop shows on the first read after the last refusal.
	p.loseNextReplyShowingAfter("stop", vmRestartMaxRefusals-1)
	h := &hookRecorder{p: p}

	out, err := p.restartWithHook(t, c, generousTime, h)

	if err == nil || !strings.Contains(err.Error(), "keeps refusing to stop VM 7") {
		t.Fatalf("err = %v, want the stop to be given up on", err)
	}
	if !strings.Contains(err.Error(), "was started again, nothing was changed") {
		t.Errorf("err = %v, want it to say that the VM was started again", err)
	}
	if out.hookRan || out.cycled || h.calls != 0 {
		t.Errorf("outcome = %+v, hook called %d times, want the work not done", out, h.calls)
	}
	wantCalls(t, p.posts(), append(callsOf("stop", vmRestartMaxRefusals), "start"))
	if got := p.current(); got == "STOPPED" {
		t.Errorf("the VM is %s at the end: nothing started it", got)
	}
}

// A stop that is refused over and over while the VM goes on running is a stop that did not happen: the
// error says so and the VM is left alone. The refusals have outlasted the patience already, so the VM
// that reads RUNNING when the cycle ends is up at once, and is not looked at for a lag a second time.
func TestRestartRunningVMWith_RefusalsOfAStopThatNeverShowsLeaveTheVMRunning(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.failCalls("stop", -1, panelRefusal)
	h := &hookRecorder{p: p}

	out, err := p.restartWithHook(t, c, generousTime, h)

	if err == nil || !strings.Contains(err.Error(), "keeps refusing to stop VM 7") {
		t.Fatalf("err = %v, want the stop to be given up on", err)
	}
	if !strings.Contains(err.Error(), "is up, nothing was changed") {
		t.Errorf("err = %v, want it to say that the VM is up", err)
	}
	if out.hookRan || h.calls != 0 {
		t.Errorf("outcome = %+v, hook called %d times, want the work not done", out, h.calls)
	}
	wantCalls(t, p.posts(), callsOf("stop", vmRestartMaxRefusals))
	if got := p.current(); got != "RUNNING" {
		t.Errorf("the VM is %s at the end, want it left RUNNING", got)
	}
	if got, want := p.statusReads(), vmRestartMaxRefusals+1; got != want {
		t.Errorf("the VM was read %d times, want %d: one before each refused stop and one that shows it is up", got, want)
	}
}

// Status reads that fail for as long as the patience, after a stop that was accepted: the VM is off
// because of us, and the cycle that gives up on the reads starts it again.
func TestRestartRunningVMWith_ReadsThatFailAfterAnAcceptedStopDoNotLeaveTheVMOff(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.failReadsLike(append([]bool{false}, slices.Repeat([]bool{true}, vmRestartMaxRefusals)...)...)
	h := &hookRecorder{p: p}

	out, err := p.restartWithHook(t, c, generousTime, h)

	if err == nil || !strings.Contains(err.Error(), "reading the status of VM 7") {
		t.Fatalf("err = %v, want the failure of the reads", err)
	}
	if !strings.Contains(err.Error(), "was started again, nothing was changed") {
		t.Errorf("err = %v, want it to say that the VM was started again", err)
	}
	if out.hookRan || h.calls != 0 {
		t.Errorf("outcome = %+v, hook called %d times, want the work not done", out, h.calls)
	}
	wantCalls(t, p.posts(), []string{"stop", "start"})
}

// Status reads that fail for as long as the patience, after a stop that was refused: the reads have
// outlasted the patience, so the VM that reads RUNNING when the cycle looks at it again is up at once,
// not waited on for the length of a lag a second time.
func TestRestartRunningVMWith_ReadsThatFailAfterARefusedStopLeaveAVMThatReadsRunningUp(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.failCalls("stop", 1, panelRefusal)
	// The first read finds the VM running and the stop is refused; every read after that fails, until
	// the patience is spent.
	p.failReadsLike(append([]bool{false}, slices.Repeat([]bool{true}, vmRestartMaxRefusals)...)...)
	h := &hookRecorder{p: p}

	out, err := p.restartWithHook(t, c, generousTime, h)

	if err == nil || !strings.Contains(err.Error(), "reading the status of VM 7") {
		t.Fatalf("err = %v, want the failure of the reads", err)
	}
	if !strings.Contains(err.Error(), "is up, nothing was changed") {
		t.Errorf("err = %v, want it to say that the VM is up", err)
	}
	if out.hookRan || h.calls != 0 {
		t.Errorf("outcome = %+v, hook called %d times, want the work not done", out, h.calls)
	}
	wantCalls(t, p.posts(), []string{"stop"})
	if got, want := p.statusReads(), vmRestartMaxRefusals+2; got != want {
		t.Errorf("the VM was read %d times, want %d: the first, one per failure, and one that shows it is up", got, want)
	}
}

// The hook has run, and the cycle then gives up on the VM that it cannot get started (the start is
// refused, and the status reads fail for as long as the patience): the work was done, so the error does
// not say that nothing was changed, and the give-up does not start the VM: the cycle's own start was
// the one.
func TestRestartRunningVMWith_AGiveUpAfterTheHookDoesNotSayThatNothingWasChanged(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.setSteps([]string{"STOPPED"}, nil)
	p.failCalls("start", 1, panelRefusal)
	// The reads that find the VM RUNNING and then STOPPED succeed, the start that follows is refused,
	// and the reads after it fail for as long as the patience.
	p.failReadsLike(append([]bool{false, false}, slices.Repeat([]bool{true}, vmRestartMaxRefusals)...)...)
	h := &hookRecorder{p: p}

	out, err := p.restartWithHook(t, c, generousTime, h)

	if err == nil || !strings.Contains(err.Error(), "reading the status of VM 7") {
		t.Fatalf("err = %v, want the failure of the reads", err)
	}
	if strings.Contains(err.Error(), "nothing was changed") || strings.Contains(err.Error(), "started again") {
		t.Errorf("err = %v: the hook has run, and the error says that it has not", err)
	}
	if !out.hookRan || h.calls != 1 {
		t.Errorf("outcome = %+v, hook called %d times, want the hook run once", out, h.calls)
	}
	wantCalls(t, p.posts(), []string{"stop", "start"})
}

// A stop that the panel refuses for good was not carried out: the VM is left as it was and the error
// does not claim to have started anything.
func TestRestartRunningVMWith_AStopRefusedForGoodLeavesTheVMAlone(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.breakCall("stop", http.StatusForbidden, 714)
	h := &hookRecorder{p: p}

	_, err := p.restartWithHook(t, c, generousTime, h)

	if err == nil || !strings.Contains(err.Error(), "stopping VM 7") {
		t.Fatalf("err = %v, want the refusal of the stop", err)
	}
	if strings.Contains(err.Error(), "started again") || strings.Contains(err.Error(), "nothing was changed") {
		t.Errorf("err = %v: the VM was not touched, and the error should not say what became of it", err)
	}
	wantCalls(t, p.posts(), []string{"stop"})
}

// The context ends while a stop of ours has been refused and the VM reads RUNNING: the panel's view may
// lag, so the VM is looked at until it shows what the stop did, and is started if it is off.
func TestRestartRunningVMWith_AnInterruptionAfterARefusedStopWaitsForTheVMToShow(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.loseNextReplyShowingAfter("stop", 30)
	h := &hookRecorder{p: p}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelWhenStopped(p, cancel)

	out, err := restartRunningVMWith(ctx, c, vmPanelVMID, &client.RequestOpts{}, restartTestPoll, h.hook)

	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the interruption", err)
	}
	if !strings.Contains(err.Error(), "was started again, nothing was changed") {
		t.Errorf("err = %v, want it to say that the VM was started again", err)
	}
	if out.hookRan || h.calls != 0 {
		t.Errorf("outcome = %+v, hook called %d times, want the work not done", out, h.calls)
	}
	if posts := p.posts(); posts[len(posts)-1] != "start" {
		t.Errorf("calls = %v, want the VM started again at the end", posts)
	}
	if got := p.current(); got == "STOPPED" {
		t.Errorf("the VM is %s at the end: nothing started it", got)
	}
}

// A stop that the panel accepted is doubted too, for as long as the VM has not been seen stopping or
// stopped: the panel's mirror of the hypervisor can write RUNNING over a stop that is taking hold, and a
// VM under the hypervisor's HA manager is shut down with a timeout first. A VM that reads RUNNING when
// the cycle ends is looked at until it has read RUNNING for as long as a lag can last, and is up then.
func TestRestartRunningVMWith_AnInterruptionAfterAnAcceptedStopLooksAtAVMThatReadsRunning(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	// The stop is accepted, and the VM reads RUNNING after it, for good.
	p.setSteps([]string{"RUNNING"}, nil)
	h := &hookRecorder{p: p}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelAtTheFirstReadAfterTheStop(p, cancel)

	out, err := restartRunningVMWith(ctx, c, vmPanelVMID, &client.RequestOpts{}, restartTestPoll, h.hook)

	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the interruption", err)
	}
	if !strings.Contains(err.Error(), "is up, nothing was changed") {
		t.Errorf("err = %v, want it to say that the VM is up", err)
	}
	if out.hookRan || h.calls != 0 {
		t.Errorf("outcome = %+v, hook called %d times, want the work not done", out, h.calls)
	}
	wantCalls(t, p.posts(), []string{"stop"})
	if got, want := p.statusReads(), 2+vmRestartMaxRefusals; got != want {
		t.Errorf("the VM was read %d times, want %d: before the stop, after it, and the patience of a refused call to see that it is up", got, want)
	}
}

// The same interruption, and the panel's view of the VM is behind: the stop was accepted, the VM reads
// RUNNING for a few reads and then STOPPED. It is off because of us, and is started again.
func TestRestartRunningVMWith_AnInterruptionAfterAnAcceptedStopStartsTheVMThatALagHid(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.setSteps([]string{"RUNNING", "RUNNING", "RUNNING", "STOPPED"}, nil)
	h := &hookRecorder{p: p}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelAtTheFirstReadAfterTheStop(p, cancel)

	out, err := restartRunningVMWith(ctx, c, vmPanelVMID, &client.RequestOpts{}, restartTestPoll, h.hook)

	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the interruption", err)
	}
	if !strings.Contains(err.Error(), "was started again, nothing was changed") {
		t.Errorf("err = %v, want it to say that the VM was started again", err)
	}
	if out.hookRan || h.calls != 0 {
		t.Errorf("outcome = %+v, hook called %d times, want the work not done", out, h.calls)
	}
	wantCalls(t, p.posts(), []string{"stop", "start"})
	if got := p.current(); got == "STOPPED" {
		t.Errorf("the VM is %s at the end: nothing started it", got)
	}
}

// The first stop is carried out and answered 627, the panel's view lags behind it, and the repeat of the
// stop is refused for good: a refusal after a stop that may have gone through is no proof that the VM
// was not stopped, and the VM that reads RUNNING is looked at until it shows what the first stop did.
func TestRestartRunningVMWith_AStopRefusedForGoodAfterALostOneStartsTheVMTheLostOneStopped(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.loseNextReplyShowingAfter("stop", 20)
	p.breakCallsAfter("stop", 1, http.StatusForbidden, 714)
	h := &hookRecorder{p: p}

	out, err := p.restartWithHook(t, c, generousTime, h)

	if err == nil || !strings.Contains(err.Error(), "stopping VM 7") {
		t.Fatalf("err = %v, want the refusal of the stop", err)
	}
	if !strings.Contains(err.Error(), "was started again, nothing was changed") {
		t.Errorf("err = %v, want it to say that the VM was started again", err)
	}
	if out.hookRan || h.calls != 0 {
		t.Errorf("outcome = %+v, hook called %d times, want the work not done", out, h.calls)
	}
	wantCalls(t, p.posts(), []string{"stop", "stop", "start"})
	if got := p.current(); got == "STOPPED" {
		t.Errorf("the VM is %s at the end: nothing started it", got)
	}
}

// The same after reads that fail for good: a few of them say nothing of how far behind the panel's view
// is, so the VM that reads RUNNING once they stop is looked at until it shows what the stop did.
func TestRestartRunningVMWith_ReadsThatFailForGoodAfterARefusedStopStillLookAtTheVM(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.loseNextReplyShowingAfter("stop", 20)
	p.failReadsWith(answer{http.StatusForbidden, 403})
	// The read that finds the VM running; the stop goes through and is answered 627; the reads that follow
	// fail, and then work again.
	p.failReadsLike(append([]bool{false}, slices.Repeat([]bool{true}, vmRestartMaxPollErrs)...)...)
	h := &hookRecorder{p: p}

	_, err := p.restartWithHook(t, c, generousTime, h)

	if want := fmt.Sprintf("after %d consecutive failures", vmRestartMaxPollErrs); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want the failed reads (%s)", err, want)
	}
	if !strings.Contains(err.Error(), "was started again, nothing was changed") {
		t.Errorf("err = %v, want it to say that the VM was started again", err)
	}
	wantCalls(t, p.posts(), []string{"stop", "start"})
	if got := p.current(); got == "STOPPED" {
		t.Errorf("the VM is %s at the end: nothing started it", got)
	}
}

// cancelAtTheReadThatShowsSTOPPED ends the context once the client has read the answer that says the VM
// is STOPPED, before the cycle acts on it: the one moment between two reads that a test can pick without
// a race with the cycle. then, when it is not nil, runs just before the context ends.
func cancelAtTheReadThatShowsSTOPPED(t *testing.T, p *vmPanel, cancel context.CancelFunc, then func()) {
	t.Helper()
	useTransport(t, func(next http.RoundTripper) http.RoundTripper {
		return afterBodyClosed{
			RoundTripper: next,
			match:        func(r *http.Request) bool { return r.Method == http.MethodGet },
			hook: func() {
				if p.current() == "STOPPED" {
					if then != nil {
						then()
					}
					cancel()
				}
			},
		}
	})
}

// The interruption comes as the read that shows the VM STOPPED is answered, after a stop that was
// refused: the VM is off because of us, the hook has not run, and the VM is started again.
func TestRestartRunningVMWith_AnInterruptionAtTheFirstSTOPPEDAfterARefusedStopStartsTheVM(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.loseNextReplyShowingAfter("stop", 2)
	h := &hookRecorder{p: p}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelAtTheReadThatShowsSTOPPED(t, p, cancel, nil)

	out, err := restartRunningVMWith(ctx, c, vmPanelVMID, &client.RequestOpts{}, restartTestPoll, h.hook)

	if err == nil || !strings.Contains(err.Error(), "waiting for VM 7 to start (last status STOPPED)") {
		t.Fatalf("err = %v: the cycle did not see STOPPED before the interruption, and the test does not test what it is for", err)
	}
	if !strings.Contains(err.Error(), "was started again, nothing was changed") {
		t.Errorf("err = %v, want it to say that the VM was started again", err)
	}
	if out.hookRan || h.calls != 0 {
		t.Fatalf("outcome = %+v, hook called %d times: the test does not test what it is for", out, h.calls)
	}
	// The stop is repeated while the VM reads RUNNING (the panel refuses it again, as the hypervisor may),
	// and the VM that then reads STOPPED is started.
	wantCalls(t, p.posts(), []string{"stop", "stop", "stop", "start"})
	if got := p.current(); got == "STOPPED" {
		t.Errorf("the VM is %s at the end: nothing started it", got)
	}
}

// The VM has been seen stopped, so there is no lag to wait out: if it reads RUNNING when the cycle ends
// (somebody started it at that very moment), it is up, and is taken for up at once.
func TestRestartRunningVMWith_AVMSeenStoppedThatReadsRunningAfterTheInterruptionIsUpAtOnce(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.loseNextReplyShowingAfter("stop", 2)
	h := &hookRecorder{p: p}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelAtTheReadThatShowsSTOPPED(t, p, cancel, func() { p.setScript("RUNNING") })

	out, err := restartRunningVMWith(ctx, c, vmPanelVMID, &client.RequestOpts{}, restartTestPoll, h.hook)

	if err == nil || !strings.Contains(err.Error(), "waiting for VM 7 to start (last status STOPPED)") {
		t.Fatalf("err = %v: the cycle did not see STOPPED before the interruption, and the test does not test what it is for", err)
	}
	if !strings.Contains(err.Error(), "is up, nothing was changed") {
		t.Errorf("err = %v, want it to say that the VM is up", err)
	}
	if out.hookRan || h.calls != 0 {
		t.Fatalf("outcome = %+v, hook called %d times: the test does not test what it is for", out, h.calls)
	}
	wantCalls(t, p.posts(), []string{"stop", "stop", "stop"})
	// The reads that found the VM running and then stopped, and the one that finds it running again.
	if got, want := p.statusReads(), 5; got != want {
		t.Errorf("the VM was read %d times, want %d", got, want)
	}
}

func TestRestartRunningVMWith_WithoutAHookAnInterruptionLeavesTheVMAlone(t *testing.T) {
	// The cycle of an attach is not the move: what it does after an interruption stays as it was.
	c, p := newVMPanel(t, "RUNNING")
	p.setSteps(slices.Concat(slices.Repeat([]string{"STOPPING"}, 100), []string{"STOPPED"}), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelWhenStopped(p, cancel)

	_, err := restartRunningVMWith(ctx, c, vmPanelVMID, &client.RequestOpts{}, restartTestPoll, nil)

	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the interruption", err)
	}
	if strings.Contains(err.Error(), "started again") {
		t.Errorf("err = %v: without a hook nothing is started after an interruption", err)
	}
	wantCalls(t, p.posts(), []string{"stop"})
}

func TestRestartRunningVMWith_NilIsRestartRunningVM(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// A deadline far inside the reserve: a cycle with a hook would refuse to stop the VM.
	cycled, err := restartRunningVM(ctx, c, vmPanelVMID, &client.RequestOpts{}, restartTestPoll)

	if err != nil || !cycled {
		t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
	}
	wantCalls(t, p.posts(), []string{"stop", "start"})
}

func TestStartAfterInterruption(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    string
		wantCalls []string
		wantNote  string
	}{
		{"a stopped VM is started", "STOPPED", []string{"start"}, "was started again, nothing was changed"},
		{"a running VM is left", "RUNNING", nil, "is up, nothing was changed"},
		{"a starting VM is left", "STARTING", nil, "is up, nothing was changed"},
		{"a VM in error is not touched", "ERROR", nil, "is in status ERROR and was not started again"},
		{"a deleted VM is not touched", "DELETED", nil, "is in status DELETED and was not started again"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := newVMPanel(t, tc.status)

			note := startAfterInterruption(context.Background(), c, vmPanelVMID, &client.RequestOpts{}, restartTestPoll, false)

			if !strings.Contains(note, tc.wantNote) {
				t.Errorf("note = %q, want it to contain %q", note, tc.wantNote)
			}
			wantCalls(t, p.posts(), tc.wantCalls)
		})
	}
}

func TestStartAfterInterruption_WaitsForTheStopToTakeHold(t *testing.T) {
	c, p := newVMPanel(t, "STOPPING")
	p.setScript("STOPPING", "STOPPING", "STOPPING", "STOPPED")

	note := startAfterInterruption(context.Background(), c, vmPanelVMID, &client.RequestOpts{}, restartTestPoll, false)

	if !strings.Contains(note, "was started again") {
		t.Errorf("note = %q, want the VM started", note)
	}
	wantCalls(t, p.posts(), []string{"start"})
	if atStop, atStart := p.statuses(); atStop != "" || atStart != "STOPPED" {
		t.Errorf("started at %q, want only once STOPPED", atStart)
	}
}

func TestStartAfterInterruption_GivesUpOnAStartThatKeepsFailing(t *testing.T) {
	c, p := newVMPanel(t, "STOPPED")
	p.breakCall("start", 403, 999)

	note := startAfterInterruption(context.Background(), c, vmPanelVMID, &client.RequestOpts{}, restartTestPoll, false)

	if !strings.Contains(note, "was not started again") || !strings.Contains(note, "999") {
		t.Errorf("note = %q, want the failure of the start", note)
	}
	if got := len(p.posts()); got != vmRestartMaxRefusals {
		t.Errorf("%d starts were tried, want %d: the patience of a refused call", got, vmRestartMaxRefusals)
	}
}

func TestStartAfterInterruption_ReadsThatFailAreWaitedOut(t *testing.T) {
	c, p := newVMPanel(t, "STOPPED")
	p.failNextReads(3)

	note := startAfterInterruption(context.Background(), c, vmPanelVMID, &client.RequestOpts{}, restartTestPoll, false)

	if !strings.Contains(note, "was started again") {
		t.Errorf("note = %q, want the VM started after the reads that failed", note)
	}
}

// A start that is refused is tried again after a pause, not at once: the time between the tries is
// what lets a panel that is still busy finish. A pause is never shorter than asked for, so the time the
// tries took is bounded from below, and the test cannot be flaky.
func TestStartAfterInterruption_WaitsBetweenTries(t *testing.T) {
	const poll, refusals = 40 * time.Millisecond, 3
	c, p := newVMPanel(t, "STOPPED")
	p.failCalls("start", refusals, panelRefusal)

	begun := time.Now()
	note := startAfterInterruption(context.Background(), c, vmPanelVMID, &client.RequestOpts{}, poll, false)
	took := time.Since(begun)

	if !strings.Contains(note, "was started again") {
		t.Errorf("note = %q, want the VM started after the refusals", note)
	}
	wantCalls(t, p.posts(), callsOf("start", refusals+1))
	if want := refusals * poll; took < want {
		t.Errorf("%d refused starts took %v, want at least %v: one pause after each", refusals, took, want)
	}
}

func TestStartAfterInterruption_StopsWithItsOwnTime(t *testing.T) {
	// The reserve is good for a minimum of vmStartReserve, so a context that ended long ago does
	// not stop it; here only the cancellation of the parent is checked, not the length of the time.
	c, p := newVMPanel(t, "STOPPED")
	parent, cancel := context.WithCancel(context.Background())
	cancel()

	note := startAfterInterruption(parent, c, vmPanelVMID, &client.RequestOpts{}, restartTestPoll, false)

	if !strings.Contains(note, "was started again") {
		t.Errorf("note = %q: a cancelled parent must not stop the start", note)
	}
	wantCalls(t, p.posts(), []string{"start"})
}

// A VM that reads RUNNING is up, unless a stop of ours may have been carried out all the same without
// the panel's view of the VM showing it yet: a refused stop takes the panel's status write back with
// it, and one that was accepted can be overwritten with RUNNING by the next pass of the mirror, so the
// VM goes on reading RUNNING until the panel next mirrors the hypervisor. Such a VM is looked at until
// it shows what the stop did.
func TestStartAgain_ADoubtfulVMThatTurnsOutToBeOffIsStarted(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.setScript("RUNNING", append(slices.Repeat([]string{"RUNNING"}, 10), "STOPPED")...)

	up, said := startAgain(context.Background(), c, vmPanelVMID, &client.RequestOpts{}, restartTestPoll, true)

	if !up || !strings.Contains(said, "was started again") {
		t.Errorf("startAgain = %v, %q, want the VM started once it showed STOPPED", up, said)
	}
	wantCalls(t, p.posts(), []string{"start"})
}

func TestStartAgain_ADoubtfulVMThatKeepsReadingRunningIsUpAfterThePatience(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")

	up, said := startAgain(context.Background(), c, vmPanelVMID, &client.RequestOpts{}, restartTestPoll, true)

	if !up || said != "VM 7 is up" {
		t.Errorf("startAgain = %v, %q, want the VM up", up, said)
	}
	if got := p.statusReads(); got != vmRestartMaxRefusals {
		t.Errorf("the VM was read %d times, want %d: the patience of a refused call", got, vmRestartMaxRefusals)
	}
	wantCalls(t, p.posts(), nil)
}

// The reads of RUNNING that make a doubtful VM up are the ones in a row: a VM that is seen stopping in
// between starts the count again, and is started once it reads STOPPED.
func TestStartAgain_ADoubtfulVMCountsTheReadsOfRunningInARow(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	// More reads of RUNNING in all than the patience, but never as many in a row.
	run := slices.Repeat([]string{"RUNNING"}, vmRestartMaxRefusals-1)
	p.setScript("RUNNING", slices.Concat(run, []string{"STOPPING"}, run, []string{"STOPPED"})...)

	up, said := startAgain(context.Background(), c, vmPanelVMID, &client.RequestOpts{}, restartTestPoll, true)

	if !up || !strings.Contains(said, "was started again") {
		t.Errorf("startAgain = %v, %q, want the VM started once it showed STOPPED", up, said)
	}
	wantCalls(t, p.posts(), []string{"start"})
}

// A read that fails says nothing of the VM, so it neither counts as a read of RUNNING nor starts the
// count again: the VM is up after as many reads of RUNNING as the patience, however many reads that
// failed came between them.
func TestStartAgain_AReadThatFailsDoesNotStartTheCountOfRunningAgain(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	// Twenty reads that work, one that fails, and then reads that work for good.
	p.failReadsLike(append(slices.Repeat([]bool{false}, 20), true)...)

	up, said := startAgain(context.Background(), c, vmPanelVMID, &client.RequestOpts{}, restartTestPoll, true)

	if !up || said != "VM 7 is up" {
		t.Errorf("startAgain = %v, %q, want the VM up", up, said)
	}
	if got, want := p.statusReads(), vmRestartMaxRefusals+1; got != want {
		t.Errorf("the VM was read %d times, want %d: the patience, and the read that failed", got, want)
	}
	wantCalls(t, p.posts(), nil)
}

func TestStartAgain_AVMThatIsNotDoubtfulIsUpAtOnce(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")

	up, said := startAgain(context.Background(), c, vmPanelVMID, &client.RequestOpts{}, restartTestPoll, false)

	if !up || said != "VM 7 is up" {
		t.Errorf("startAgain = %v, %q, want the VM up", up, said)
	}
	if got := p.statusReads(); got != 1 {
		t.Errorf("the VM was read %d times, want once", got)
	}
}

// A VM that is starting is up whatever the doubt: somebody started it, or the start went through.
func TestStartAgain_ADoubtfulVMThatIsStartingIsUpAtOnce(t *testing.T) {
	c, p := newVMPanel(t, "STARTING")

	up, _ := startAgain(context.Background(), c, vmPanelVMID, &client.RequestOpts{}, restartTestPoll, true)

	if !up || p.statusReads() != 1 {
		t.Errorf("startAgain = %v after %d reads, want the VM up after one", up, p.statusReads())
	}
}

func TestHookBudget(t *testing.T) {
	if got := hookBudget(context.Background()); got < 1000*time.Hour {
		t.Errorf("hookBudget(no deadline) = %v, want all the time there is", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	if got, want := hookBudget(ctx), 12*time.Minute; got > want || got < want-time.Second {
		t.Errorf("hookBudget(20m) = %v, want about %v", got, want)
	}
	past, cancelPast := context.WithTimeout(context.Background(), -time.Minute)
	defer cancelPast()
	if got := hookBudget(past); got >= 0 {
		t.Errorf("hookBudget(passed) = %v, want negative", got)
	}
}

func TestHookContext(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	deadline, _ := parent.Deadline()

	ctx, cancelHook := hookContext(parent)
	defer cancelHook()

	got, ok := ctx.Deadline()
	if want := deadline.Add(-vmStartReserve); !ok || got.Sub(want) > time.Millisecond || want.Sub(got) > time.Millisecond {
		t.Errorf("deadline = %v (%v), want %v", got, ok, want)
	}
	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Error("the context of the hook outlived the cancellation of its parent: the user could not interrupt it")
	}

	free, cancelFree := hookContext(context.Background())
	defer cancelFree()
	if _, ok := free.Deadline(); ok {
		t.Error("a parent without a deadline gave the hook one")
	}
}

func TestReserveContext(t *testing.T) {
	t.Run("it outlives the cancellation of its parent", func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		reserve, cancelReserve := reserveContext(parent)
		defer cancelReserve()

		cancel()

		if err := reserve.Err(); err != nil {
			t.Errorf("reserve.Err() = %v after the parent was cancelled, want nil", err)
		}
	})

	t.Run("it is good for the reserve when the parent has run out", func(t *testing.T) {
		parent, cancel := context.WithTimeout(context.Background(), -time.Minute)
		defer cancel()
		reserve, cancelReserve := reserveContext(parent)
		defer cancelReserve()

		deadline, ok := reserve.Deadline()
		if !ok || time.Until(deadline) > vmStartReserve || time.Until(deadline) < vmStartReserve-time.Second {
			t.Errorf("deadline in %v (%v), want about %v", time.Until(deadline), ok, vmStartReserve)
		}
	})

	t.Run("it keeps the time of the parent when that is longer", func(t *testing.T) {
		parent, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		reserve, cancelReserve := reserveContext(parent)
		defer cancelReserve()

		deadline, _ := reserve.Deadline()
		if left := time.Until(deadline); left < 19*time.Minute {
			t.Errorf("deadline in %v, want about 20m: the update was given that long", left)
		}
	})

	t.Run("a parent without a deadline gets the reserve", func(t *testing.T) {
		reserve, cancelReserve := reserveContext(context.Background())
		defer cancelReserve()

		deadline, ok := reserve.Deadline()
		if !ok || time.Until(deadline) > vmStartReserve {
			t.Errorf("deadline in %v (%v), want at most %v", time.Until(deadline), ok, vmStartReserve)
		}
	})
}
