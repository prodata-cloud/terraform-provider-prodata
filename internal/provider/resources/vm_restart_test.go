package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"terraform-provider-prodata/internal/client"
)

// vmPanel is a minimal panel-main with one VM, id 7: enough of /api/v2/vms/{id} to attach and
// detach a public IP and to stop and start the VM. It refuses a stop or a start the way the real
// panel does when the VM is in the wrong status, and any request it does not know is a test
// failure.
type vmPanel struct {
	mu sync.Mutex

	status string   // the status the panel reports now
	script []string // statuses the next status reads report, one per read, before status settles

	// What the status reads that follow an accepted stop or start report.
	stopSteps, startSteps []string

	publicIPID int64    // the attached public IP, 0 for none
	requests   []string // "METHOD path" in arrival order, path relative to /panel-main
	queries    []string // the query string of each request in requests

	// refusals maps an operation ("stop", "start", "attach") to the number of calls the panel
	// answers with 627 before it accepts one, or with refusalAnswer when that has an entry for the
	// operation; a negative number means every call.
	refusals      map[string]int
	refusalAnswer map[string]answer
	// broken maps an operation to an error it always answers with instead: its HTTP status and code.
	broken map[string][2]int
	// brokenAfter maps an operation to an error it answers with instead once the panel has been asked
	// for it a number of times: the calls before that are served as they would be.
	brokenAfter map[string]brokenFrom
	// lostReplies maps an operation ("stop", "start") to the number of calls the panel carries out
	// and then answers with 627 all the same, as it does when a hop behind it times out or when it
	// fails after the hypervisor call, or with lostAnswer when that has an entry for the operation;
	// a negative number means every call.
	lostReplies map[string]int
	lostAnswer  map[string]answer
	// hanging holds the calls of an operation until the client gives up on them.
	hanging map[string]bool
	// lostLag, when positive, makes a lost reply behave as the real panel's can: the refusal takes
	// the panel's status write back with it, so the status reads go on reporting the old status for
	// lostLag more reads, until the panel's mirror of the hypervisor catches up. Meanwhile a repeat of
	// the call is refused, as the hypervisor may well refuse it (a stop of a VM that is off, a start
	// of one that is running) and the panel then reports 627 again: the worst case for a client that
	// retries. With no lag the effect shows at once.
	lostLag     int
	lostPending map[string]bool
	// readFailures says, for each upcoming status read in turn, whether it fails, with readAnswer
	// (a 500 with code 627 unless the test says otherwise).
	readFailures []bool
	readAnswer   answer

	// The VM's status at the moment a stop, a start or an attach was accepted.
	statusAtStop, statusAtStart, statusAtAttach string

	// extra serves the requests the power and attach calls do not cover, for the tests of more than
	// the restart: it answers and returns true, or returns false for a request it does not know. It
	// runs with the panel's lock held. bodyExtra adds fields to the VM that the status reads report.
	extra     func(w http.ResponseWriter, r *http.Request) bool
	bodyExtra func(body map[string]any)
}

// answer is a failure the panel answers with: an HTTP status and a panel error code. Code 0 is a
// bare gateway page with no panel envelope in it, and status 0 a connection that is dropped.
type answer struct{ status, code int }

// brokenFrom is the failure an operation gets from its call number calls+1 on.
type brokenFrom struct {
	calls  int
	status int
	code   int
}

var (
	panelRefusal   = answer{http.StatusInternalServerError, 627}
	gatewayTimeout = answer{http.StatusGatewayTimeout, 0}
	badGateway     = answer{http.StatusBadGateway, 0}
	droppedAnswer  = answer{0, 0}
	noCompute      = answer{http.StatusServiceUnavailable, 744}
)

const (
	vmPanelVMID    = 7
	vmPanelBase    = "/panel-main/api/v2/vms/7"
	vmPanelAddress = "203.0.113.10"
)

func newVMPanel(t *testing.T, status string) (*client.Client, *vmPanel) {
	t.Helper()
	p := &vmPanel{
		status:        status,
		stopSteps:     []string{"STOPPING", "STOPPED"},
		startSteps:    []string{"STARTING", "RUNNING"},
		refusals:      map[string]int{},
		refusalAnswer: map[string]answer{},
		broken:        map[string][2]int{},
		brokenAfter:   map[string]brokenFrom{},
		lostReplies:   map[string]int{},
		lostAnswer:    map[string]answer{},
		lostPending:   map[string]bool{},
		hanging:       map[string]bool{},
		readAnswer:    panelRefusal,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.serve(t, w, r)
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(client.Config{APIBaseURL: srv.URL, APIKeyID: "k", APISecretKey: "s", Region: "TEST", ProjectTag: "proj"})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	return c, p
}

func (p *vmPanel) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	// Whatever is not the panel's API was not sent by the code under test: a stray process probing
	// the loopback port is no failure of it.
	if !strings.HasPrefix(r.URL.Path, "/panel-main/api/") {
		http.NotFound(w, r)
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, r.Method+" "+strings.TrimPrefix(r.URL.Path, "/panel-main"))
	p.queries = append(p.queries, r.URL.RawQuery)

	switch {
	case r.Method == http.MethodGet && r.URL.Path == vmPanelBase+"/status":
		if len(p.readFailures) > 0 {
			failing := p.readFailures[0]
			p.readFailures = p.readFailures[1:]
			if failing {
				p.failWith(w, p.readAnswer)
				return
			}
		}
		if p.hold(r, "status") {
			return
		}
		if len(p.script) > 0 {
			p.status, p.script = p.script[0], p.script[1:]
			if len(p.script) == 0 {
				clear(p.lostPending)
			}
		}
		p.ok(w, p.vmBody())

	case r.Method == http.MethodGet && r.URL.Path == vmPanelBase:
		p.ok(w, p.vmBody())

	case r.Method == http.MethodPost && r.URL.Path == vmPanelBase+"/stop":
		if p.hold(r, "stop") {
			return
		}
		// The real panel: 627 for a VM that is stopping, stopped, starting or rebooting.
		p.power(w, "stop", []string{"STOPPING", "STOPPED", "STARTING", "REBOOTING"}, "STOPPING", p.stopSteps, &p.statusAtStop)

	case r.Method == http.MethodPost && r.URL.Path == vmPanelBase+"/start":
		if p.hold(r, "start") {
			return
		}
		// The real panel: 627 for a VM that is starting, running, stopping or rebooting.
		p.power(w, "start", []string{"STARTING", "RUNNING", "STOPPING", "REBOOTING"}, "STARTING", p.startSteps, &p.statusAtStart)

	case r.Method == http.MethodPost && r.URL.Path == vmPanelBase+"/public-ip":
		if p.refused(w, "attach") {
			return
		}
		var body struct {
			PublicIPID int64 `json:"publicIpId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("attach body: %v", err)
		}
		p.statusAtAttach = p.status
		p.publicIPID = body.PublicIPID
		p.ok(w, p.vmBody())

	case r.Method == http.MethodDelete && r.URL.Path == vmPanelBase+"/public-ip":
		p.publicIPID = 0
		p.ok(w, nil)

	default:
		if p.extra != nil && p.extra(w, r) {
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}
}

// refused answers a call the test told the panel to fail, and reports whether it did.
func (p *vmPanel) refused(w http.ResponseWriter, op string) bool {
	if e, ok := p.broken[op]; ok {
		p.fail(w, e[0], e[1])
		return true
	}
	if e, ok := p.brokenAfter[op]; ok && p.callsMade(op) > e.calls {
		p.fail(w, e.status, e.code)
		return true
	}
	if n := p.refusals[op]; n != 0 {
		if n > 0 {
			p.refusals[op]--
		}
		a, ok := p.refusalAnswer[op]
		if !ok {
			a = panelRefusal
		}
		p.failWith(w, a)
		return true
	}
	return false
}

// hold keeps a call the test told the panel to hang until the client gives up on it, and reports
// whether it did. The panel's lock is held meanwhile: nothing else is in flight in such a test.
func (p *vmPanel) hold(r *http.Request, op string) bool {
	if !p.hanging[op] {
		return false
	}
	<-r.Context().Done()
	return true
}

// power serves a stop or a start: 627 when the test says so or when the VM is in a status the
// call is not allowed from, otherwise accepted, with the status reads that follow scripted.
func (p *vmPanel) power(w http.ResponseWriter, op string, notFrom []string, now string, then []string, seenAt *string) {
	if p.refused(w, op) {
		return
	}
	if slices.Contains(notFrom, p.status) || p.lostPending[op] {
		p.fail(w, http.StatusInternalServerError, 627)
		return
	}
	*seenAt = p.status
	if n := p.lostReplies[op]; n != 0 {
		if n > 0 {
			p.lostReplies[op]--
		}
		if p.lostLag > 0 {
			// Carried out, but the status write is rolled back: the reads show the old status
			// for a while and then the status the effect settles in.
			p.script = append(slices.Repeat([]string{p.status}, p.lostLag), then[len(then)-1])
			p.lostPending[op] = true
		} else {
			p.status, p.script = now, slices.Clone(then)
		}
		a, ok := p.lostAnswer[op]
		if !ok {
			a = panelRefusal
		}
		p.failWith(w, a)
		return
	}
	p.status, p.script = now, slices.Clone(then)
	p.ok(w, nil)
}

func (p *vmPanel) vmBody() map[string]any {
	body := map[string]any{"id": vmPanelVMID, "guid": "g", "name": "vm", "status": p.status}
	if p.publicIPID != 0 {
		body["publicIp"], body["publicIpId"] = vmPanelAddress, p.publicIPID
	}
	if p.bodyExtra != nil {
		p.bodyExtra(body)
	}
	return body
}

func (p *vmPanel) ok(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
}

func (p *vmPanel) fail(w http.ResponseWriter, httpStatus, code int) {
	p.failWith(w, answer{httpStatus, code})
}

// failWith answers with a failure: the panel's envelope, a bare gateway page, or nothing at all
// and a dropped connection.
func (p *vmPanel) failWith(w http.ResponseWriter, a answer) {
	switch {
	case a.status == 0:
		panic(http.ErrAbortHandler)
	case a.code == 0:
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(a.status)
		_, _ = w.Write([]byte("<html><body>" + http.StatusText(a.status) + "</body></html>"))
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(a.status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false, "data": nil,
			"errors": []map[string]any{{"code": a.code, "message": "refused"}},
		})
	}
}

// posts returns the power and attach calls made so far, in order, as "stop", "start", "attach"
// and "detach".
func (p *vmPanel) posts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, r := range p.requests {
		switch r {
		case "POST /api/v2/vms/7/stop":
			out = append(out, "stop")
		case "POST /api/v2/vms/7/start":
			out = append(out, "start")
		case "POST /api/v2/vms/7/public-ip":
			out = append(out, "attach")
		case "DELETE /api/v2/vms/7/public-ip":
			out = append(out, "detach")
		}
	}
	return out
}

// statusReads is how many times the panel was asked for the status of the VM.
func (p *vmPanel) statusReads() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, request := range p.requests {
		if request == "GET /api/v2/vms/7/status" {
			n++
		}
	}
	return n
}

func (p *vmPanel) setScript(status string, script ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.status, p.script = status, script
}

// setSteps replaces the statuses the status reads report after an accepted stop and start; nil
// leaves the ones in place.
func (p *vmPanel) setSteps(stop, start []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if stop != nil {
		p.stopSteps = stop
	}
	if start != nil {
		p.startSteps = start
	}
}

func (p *vmPanel) refuseCalls(op string, times int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refusals[op] = times
}

// failCalls makes the next n calls of op (every call, for a negative n) fail before they are
// carried out, with a.
func (p *vmPanel) failCalls(op string, n int, a answer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refusals[op], p.refusalAnswer[op] = n, a
}

// hangCalls makes the panel hold every call of op ("stop", "start", or "status" for the status reads
// that would otherwise succeed) until the client gives up on it.
func (p *vmPanel) hangCalls(op string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hanging[op] = true
}

func (p *vmPanel) breakCall(op string, httpStatus, code int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.broken[op] = [2]int{httpStatus, code}
}

// breakCallsAfter lets the first n calls of op ("stop" or "start") be served as they would be and
// answers every later one, before it is carried out, with the status and code given: the panel's last
// word on a call that came after one that was carried out.
func (p *vmPanel) breakCallsAfter(op string, n, httpStatus, code int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.brokenAfter[op] = brokenFrom{calls: n, status: httpStatus, code: code}
}

// callsMade is how many calls of op ("stop" or "start") the panel has been asked for, the one being
// served included. The panel's lock must be held.
func (p *vmPanel) callsMade(op string) int {
	n := 0
	for _, request := range p.requests {
		if request == "POST /api/v2/vms/7/"+op {
			n++
		}
	}
	return n
}

// failNextReads makes the next n status reads fail.
func (p *vmPanel) failNextReads(n int) {
	p.failReadsLike(slices.Repeat([]bool{true}, n)...)
}

// failReadsWith sets how a status read that fails answers.
func (p *vmPanel) failReadsWith(a answer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.readAnswer = a
}

// current is the status the panel reports now.
func (p *vmPanel) current() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

// failReadsLike makes the status reads that follow fail or succeed as the pattern says.
func (p *vmPanel) failReadsLike(pattern ...bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.readFailures = pattern
}

// loseNextReply makes the panel carry out the next call of op and answer it with 627.
func (p *vmPanel) loseNextReply(op string) {
	p.loseNextReplyAs(op, panelRefusal)
}

// loseNextReplyAs makes the panel carry out the next call of op and answer it with a.
func (p *vmPanel) loseNextReplyAs(op string, a answer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lostReplies[op], p.lostAnswer[op] = 1, a
}

// loseNextReplyShowingAfter is loseNextReply for a panel whose status catches up late: the effect of
// the call shows only after the VM has been read lag more times as it was before the call.
func (p *vmPanel) loseNextReplyShowingAfter(op string, lag int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lostReplies[op] = 1
	p.lostLag = lag
}

func (p *vmPanel) statuses() (atStop, atStart string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.statusAtStop, p.statusAtStart
}

const restartTestPoll = time.Millisecond

// restartWithin runs restartRunningVM against the panel with a deadline of d.
func (p *vmPanel) restartWithin(t *testing.T, c *client.Client, d time.Duration) (bool, error) {
	t.Helper()
	return p.restartWithinPoll(t, c, d, restartTestPoll)
}

// restartWithinPoll is restartWithin with its own poll interval.
func (p *vmPanel) restartWithinPoll(t *testing.T, c *client.Client, d, poll time.Duration) (bool, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	cycled, err := restartRunningVM(ctx, c, vmPanelVMID, &client.RequestOpts{}, poll)
	// Whatever the way there, a VM that is reported cycled is RUNNING: the promise Create makes to
	// whatever depends on the attachment.
	if cycled && err == nil {
		if got := p.current(); got != "RUNNING" {
			t.Errorf("restartRunningVM reported the VM cycled while it is %s, want RUNNING", got)
		}
	}
	return cycled, err
}

// callsOf is n calls of op in a row.
func callsOf(op string, n int) []string { return slices.Repeat([]string{op}, n) }

// wantCalls asserts the power and attach calls the panel has received so far.
func wantCalls(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
}

func TestRestartRunningVM_RunningVMIsStoppedThenStarted(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")

	cycled, err := p.restartWithin(t, c, 5*time.Second)
	if err != nil || !cycled {
		t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
	}
	wantCalls(t, p.posts(), []string{"stop", "start"})
	if atStop, atStart := p.statuses(); atStop != "RUNNING" || atStart != "STOPPED" {
		t.Errorf("stopped at %q and started at %q; want stop while RUNNING and start only once STOPPED", atStop, atStart)
	}
}

func TestRestartRunningVM_VMThatIsNotUpIsLeftAlone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		script []string
	}{
		{"stopped", "STOPPED", nil},
		{"stopping, then stopped", "STOPPING", []string{"STOPPING", "STOPPED"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := newVMPanel(t, tc.status)
			p.setScript(tc.status, tc.script...)

			cycled, err := p.restartWithin(t, c, 5*time.Second)
			if err != nil || cycled {
				t.Fatalf("restartRunningVM = %v, %v; want false, nil", cycled, err)
			}
			wantCalls(t, p.posts(), nil)
		})
	}
}

func TestRestartRunningVM_StartingVMIsWaitedForThenRestarted(t *testing.T) {
	for _, status := range []string{"STARTING", "REBOOTING", "CREATING"} {
		t.Run(status, func(t *testing.T) {
			c, p := newVMPanel(t, status)
			p.setScript(status, status, status, "RUNNING")

			cycled, err := p.restartWithin(t, c, 5*time.Second)
			if err != nil || !cycled {
				t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
			}
			wantCalls(t, p.posts(), []string{"stop", "start"})
			if atStop, _ := p.statuses(); atStop != "RUNNING" {
				t.Errorf("stopped at %q, want RUNNING: a VM that is still coming up must be left to finish", atStop)
			}
		})
	}
}

func TestRestartRunningVM_VMInAnUnusableStatusFailsAtOnce(t *testing.T) {
	for _, status := range []string{"ERROR", "DELETED"} {
		t.Run(status, func(t *testing.T) {
			c, p := newVMPanel(t, status)

			_, err := p.restartWithin(t, c, 5*time.Second)
			if err == nil || !strings.Contains(err.Error(), status) {
				t.Fatalf("err = %v, want one naming status %s", err, status)
			}
			wantCalls(t, p.posts(), nil)
		})
	}
}

func TestRestartRunningVM_RefusedStopIsRetried(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.refuseCalls("stop", vmRestartMaxRefusals-1)

	cycled, err := p.restartWithin(t, c, 5*time.Second)
	if err != nil || !cycled {
		t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
	}
	wantCalls(t, p.posts(), slices.Concat(callsOf("stop", vmRestartMaxRefusals), []string{"start"}))
}

func TestRestartRunningVM_RefusedStopIsGivenUpOn(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.refuseCalls("stop", -1)

	_, err := p.restartWithin(t, c, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "refusing to stop VM 7") {
		t.Fatalf("err = %v, want the panel's refusal to stop", err)
	}
	// The limit and out, not a wait for the deadline.
	wantCalls(t, p.posts(), callsOf("stop", vmRestartMaxRefusals))
}

func TestRestartRunningVM_OnlyRefusalsAtAnUnchangedStatusAreCounted(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	// The panel refuses as many stops in all as it takes to give up, but the VM is rebooted by
	// someone else before the last of them: that is a change, not a refusal that will never go away.
	p.refuseCalls("stop", vmRestartMaxRefusals)
	p.setScript("RUNNING", append(callsOf("RUNNING", vmRestartMaxRefusals-1), "REBOOTING", "RUNNING", "RUNNING")...)

	cycled, err := p.restartWithin(t, c, 5*time.Second)
	if err != nil || !cycled {
		t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
	}
	wantCalls(t, p.posts(), slices.Concat(callsOf("stop", vmRestartMaxRefusals+1), []string{"start"}))
}

func TestRestartRunningVM_RefusedStartIsGivenUpOn(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.refuseCalls("start", -1)

	_, err := p.restartWithin(t, c, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "refusing to start VM 7") {
		t.Fatalf("err = %v, want the panel's refusal to start", err)
	}
	wantCalls(t, p.posts(), slices.Concat([]string{"stop"}, callsOf("start", vmRestartMaxRefusals)))
}

func TestRestartRunningVM_AnyOtherErrorFromStopOrStartFailsAtOnce(t *testing.T) {
	for _, op := range []string{"stop", "start"} {
		for _, a := range []answer{{http.StatusForbidden, 403}, {http.StatusNotFound, 0}, {http.StatusConflict, 7}} {
			t.Run(fmt.Sprintf("%s/%d", op, a.status), func(t *testing.T) {
				c, p := newVMPanel(t, "RUNNING")
				p.breakCall(op, a.status, a.code)

				_, err := p.restartWithin(t, c, 5*time.Second)
				if err == nil || !strings.Contains(err.Error(), fmt.Sprint(a.status)) || errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("err = %v, want the panel's %d", err, a.status)
				}
				want := []string{"stop"}
				if op == "start" {
					want = []string{"stop", "start"}
				}
				wantCalls(t, p.posts(), want)
			})
		}
	}
}

func TestRestartRunningVM_VMThatNeverStopsTimesOut(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.mu.Lock()
	p.stopSteps = []string{"RUNNING"}
	p.mu.Unlock()

	_, err := p.restartWithin(t, c, 200*time.Millisecond)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) ||
		!strings.Contains(err.Error(), "timed out waiting for VM 7 to stop") || !strings.Contains(err.Error(), "last status RUNNING") {
		t.Fatalf("err = %v, want a timeout waiting for the VM to stop", err)
	}
	wantCalls(t, p.posts(), []string{"stop"})
}

func TestRestartRunningVM_VMThatNeverStartsTimesOut(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.mu.Lock()
	p.startSteps = []string{"STARTING"}
	p.mu.Unlock()

	_, err := p.restartWithin(t, c, 200*time.Millisecond)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) ||
		!strings.Contains(err.Error(), "timed out waiting for VM 7 to start") || !strings.Contains(err.Error(), "last status STARTING") {
		t.Fatalf("err = %v, want a timeout waiting for the VM to start", err)
	}
	wantCalls(t, p.posts(), []string{"stop", "start"})
}

func TestRestartRunningVM_VMThatFailsWhileStartingIsAnError(t *testing.T) {
	for _, status := range []string{"ERROR", "DELETED"} {
		t.Run(status, func(t *testing.T) {
			c, p := newVMPanel(t, "RUNNING")
			p.setSteps(nil, []string{"STARTING", status})

			_, err := p.restartWithin(t, c, 5*time.Second)
			if err == nil || !strings.Contains(err.Error(), "went to status "+status+" while it was starting") ||
				errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err = %v, want the VM's failure to start, not a timeout", err)
			}
			wantCalls(t, p.posts(), []string{"stop", "start"})
		})
	}
}

func TestRestartRunningVM_VMThatFailsAfterTheStopIsAnErrorAndIsNotStarted(t *testing.T) {
	for _, status := range []string{"ERROR", "DELETED"} {
		t.Run(status, func(t *testing.T) {
			c, p := newVMPanel(t, "RUNNING")
			p.mu.Lock()
			p.stopSteps = []string{"STOPPING", status}
			p.mu.Unlock()

			_, err := p.restartWithin(t, c, 5*time.Second)
			if err == nil || !strings.Contains(err.Error(), "went to status "+status+" after it was stopped") {
				t.Fatalf("err = %v, want the VM's failure after the stop", err)
			}
			wantCalls(t, p.posts(), []string{"stop"})
		})
	}
}

func TestRestartRunningVM_VMThatNeverSettlesTimesOutWithoutTouchingIt(t *testing.T) {
	c, p := newVMPanel(t, "STARTING")

	_, err := p.restartWithin(t, c, 200*time.Millisecond)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) ||
		!strings.Contains(err.Error(), "timed out waiting for VM 7 to settle") || !strings.Contains(err.Error(), "last status STARTING") {
		t.Fatalf("err = %v, want a timeout waiting for the VM to settle", err)
	}
	wantCalls(t, p.posts(), nil)
}

func TestRestartRunningVM_VMThatIsStartedByAnotherPartyIsNotStartedAgain(t *testing.T) {
	for _, status := range []string{"STARTING", "REBOOTING"} {
		t.Run(status, func(t *testing.T) {
			c, p := newVMPanel(t, "RUNNING")
			// The VM is coming up for several reads: the restart is not over before it is RUNNING
			// (restartWithin checks that the VM is when it reports it cycled).
			p.setSteps([]string{"STOPPING", status, status, status, "RUNNING"}, nil)

			cycled, err := p.restartWithin(t, c, 5*time.Second)
			if err != nil || !cycled {
				t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
			}
			wantCalls(t, p.posts(), []string{"stop"})
		})
	}
}

func TestRestartRunningVM_FailedStatusReadsAreTolerated(t *testing.T) {
	t.Run("up to the limit minus one in a row", func(t *testing.T) {
		c, p := newVMPanel(t, "RUNNING")
		p.failNextReads(vmRestartMaxPollErrs - 1)

		cycled, err := p.restartWithin(t, c, 5*time.Second)
		if err != nil || !cycled {
			t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
		}
	})
	t.Run("isolated failures never add up", func(t *testing.T) {
		// A failure that may pass is waited out for longer once a stop of ours may have taken effect;
		// a final one (the panel's last word) keeps the limit everywhere.
		for _, tc := range []struct {
			name string
			fail answer
		}{
			{"that may pass", panelRefusal},
			{"that are final", answer{http.StatusForbidden, 403}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				c, p := newVMPanel(t, "RUNNING")
				p.failReadsWith(tc.fail)
				// More failures over the whole restart than the limit, but never that many in a row.
				p.failReadsLike(true, false, true, false, true, false, true)

				cycled, err := p.restartWithin(t, c, 5*time.Second)
				if err != nil || !cycled {
					t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
				}
				wantCalls(t, p.posts(), []string{"stop", "start"})
			})
		}
	})
	t.Run("isolated failures before the stop never add up", func(t *testing.T) {
		c, p := newVMPanel(t, "STARTING")
		// A VM that is starting is waited for before it is stopped: more failed reads on the way than
		// the limit, but never two in a row.
		p.setScript("STARTING", "STARTING", "STARTING", "STARTING", "STARTING", "RUNNING")
		p.failReadsLike(true, false, true, false, true, false, true, false)

		cycled, err := p.restartWithin(t, c, 5*time.Second)
		if err != nil || !cycled {
			t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
		}
		wantCalls(t, p.posts(), []string{"stop", "start"})
	})
	t.Run("isolated failures after the stop never add up to the patience", func(t *testing.T) {
		c, p := newVMPanel(t, "RUNNING")
		// A stop that is slow to show, read through a gateway that fails every other read: more
		// failures than the patience allows in a row, but never two in a row.
		isolated := vmRestartMaxRefusals + 3
		p.setSteps(append(slices.Repeat([]string{"STOPPING"}, isolated), "STOPPED"), nil)
		p.failReadsLike(append([]bool{false}, slices.Repeat([]bool{true, false}, isolated)...)...)

		cycled, err := p.restartWithin(t, c, 10*time.Second)
		if err != nil || !cycled {
			t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
		}
		wantCalls(t, p.posts(), []string{"stop", "start"})
	})
	t.Run("the limit in a row is final", func(t *testing.T) {
		c, p := newVMPanel(t, "RUNNING")
		p.failNextReads(vmRestartMaxPollErrs)

		_, err := p.restartWithin(t, c, 5*time.Second)
		if err == nil || !strings.Contains(err.Error(), "after 3 consecutive failures") {
			t.Fatalf("err = %v, want the failed reads", err)
		}
		wantCalls(t, p.posts(), nil)
	})
}

func TestRestartRunningVM_CancellationEndsTheWait(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.mu.Lock()
	p.stopSteps = []string{"RUNNING"}
	p.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	done := make(chan error, 1)
	go func() {
		_, err := restartRunningVM(ctx, c, vmPanelVMID, &client.RequestOpts{}, restartTestPoll)
		done <- err
	}()

	select {
	case err := <-done:
		// Ctrl-C is an interruption, not a timeout: the message must not blame the deadline.
		if !errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
			!strings.Contains(err.Error(), "interrupted waiting for VM 7 to stop") || strings.Contains(err.Error(), "timed out") {
			t.Fatalf("err = %v, want an interruption (context.Canceled), not a timeout", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("restartRunningVM ignored the cancellation")
	}
}

// The panel answers 627 both for a call it refused and for a call it carried out and then failed to
// report (the read timeout of a hop behind it, an exception after the hypervisor call). The
// tests below pin that a refusal is judged by what the VM does next.

func TestRestartRunningVM_StopWhoseReplyWasLostIsCarriedOn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		steps []string
	}{
		{"stopping, then stopped", []string{"STOPPING", "STOPPED"}},
		{"stopped at once", []string{"STOPPED"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := newVMPanel(t, "RUNNING")
			p.loseNextReply("stop")
			p.setSteps(tc.steps, nil)

			cycled, err := p.restartWithin(t, c, 5*time.Second)
			if err != nil || !cycled {
				t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
			}
			// The VM is off because of our stop, so it has to be started again, not left off.
			wantCalls(t, p.posts(), []string{"stop", "start"})
			if _, atStart := p.statuses(); atStart != "STOPPED" {
				t.Errorf("started at %q, want STOPPED", atStart)
			}
		})
	}
}

func TestRestartRunningVM_VMThatIsStartedAfterALostStopIsNotStartedAgain(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.loseNextReply("stop")
	// Stopped by our stop, then started by somebody else before the next read.
	p.setSteps([]string{"STOPPED", "RUNNING"}, nil)

	cycled, err := p.restartWithin(t, c, 5*time.Second)
	if err != nil || !cycled {
		t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
	}
	wantCalls(t, p.posts(), []string{"stop"})
}

func TestRestartRunningVM_ARefusedStopThatTheVMExplainsIsForgotten(t *testing.T) {
	for _, status := range []string{"STARTING", "REBOOTING", "CREATING"} {
		t.Run(status, func(t *testing.T) {
			c, p := newVMPanel(t, "RUNNING")
			p.refuseCalls("stop", 1)
			// The stop is refused, and the VM turns out to be in the middle of something else; it is
			// stopped when we look next. Nothing of ours stopped it, so it is left as it is.
			p.setScript("RUNNING", "RUNNING", status, "STOPPED")

			cycled, err := p.restartWithin(t, c, 5*time.Second)
			if err != nil || cycled {
				t.Fatalf("restartRunningVM = %v, %v; want false, nil", cycled, err)
			}
			wantCalls(t, p.posts(), []string{"stop"})
		})
	}
}

func TestRestartRunningVM_StartWhoseReplyWasLostIsCarriedOn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		steps []string
	}{
		{"starting, then running", []string{"STARTING", "RUNNING"}},
		{"running at once", []string{"RUNNING"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := newVMPanel(t, "RUNNING")
			p.loseNextReply("start")
			p.setSteps(nil, tc.steps)

			cycled, err := p.restartWithin(t, c, 5*time.Second)
			if err != nil || !cycled {
				t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
			}
			// One start: the VM came up, so the start is not asked for again.
			wantCalls(t, p.posts(), []string{"stop", "start"})
		})
	}
}

// A refusal takes the panel's own status write back with it, and the panel's status catches up with
// the hypervisor only when it next mirrors it. Until then the VM reads as it did before the call, and a
// repeat of the call is refused by the hypervisor itself: the restart has to be patient for as long
// as that takes, but not for ever.

func TestRestartRunningVM_StopWhoseEffectShowsLateIsWaitedFor(t *testing.T) {
	for _, lag := range []int{1, vmRestartMaxRefusals - 2} {
		t.Run(fmt.Sprintf("%d reads", lag), func(t *testing.T) {
			c, p := newVMPanel(t, "RUNNING")
			p.loseNextReplyShowingAfter("stop", lag)

			cycled, err := p.restartWithin(t, c, 5*time.Second)
			if err != nil || !cycled {
				t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
			}
			// The VM is off because of our stop, so it is started, once.
			wantCalls(t, p.posts(), slices.Concat(callsOf("stop", 1+lag), []string{"start"}))
			if _, atStart := p.statuses(); atStart != "STOPPED" {
				t.Errorf("started at %q, want STOPPED", atStart)
			}
		})
	}
}

func TestRestartRunningVM_StartWhoseEffectShowsLateIsWaitedFor(t *testing.T) {
	for _, lag := range []int{1, vmRestartMaxRefusals - 2} {
		t.Run(fmt.Sprintf("%d reads", lag), func(t *testing.T) {
			c, p := newVMPanel(t, "RUNNING")
			p.loseNextReplyShowingAfter("start", lag)

			cycled, err := p.restartWithin(t, c, 5*time.Second)
			if err != nil || !cycled {
				t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
			}
			wantCalls(t, p.posts(), slices.Concat([]string{"stop"}, callsOf("start", 1+lag)))
		})
	}
}

func TestRestartRunningVM_AnEffectThatNeverShowsIsGivenUpOn(t *testing.T) {
	for _, op := range []string{"stop", "start"} {
		t.Run(op, func(t *testing.T) {
			c, p := newVMPanel(t, "RUNNING")
			p.loseNextReplyShowingAfter(op, 10*vmRestartMaxRefusals)

			_, err := p.restartWithin(t, c, 5*time.Second)
			if err == nil || !strings.Contains(err.Error(), "refusing to "+op+" VM 7") || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err = %v, want the panel's refusal to %s, not a timeout", err, op)
			}
			want := callsOf(op, vmRestartMaxRefusals)
			if op == "start" {
				want = slices.Concat([]string{"stop"}, want)
			}
			wantCalls(t, p.posts(), want)
		})
	}
}

// The panel mirrors the hypervisor in two passes, each every 20 seconds or so, and a pass works from
// what the one before it left: the status of a VM can show what a call did only over a minute
// later (about 2×(20 s + a pass) + 20 s + a pass, and a pass takes seconds in a busy region), and the
// status read that finds it comes a poll interval after the call. A refusal takes the panel's own
// status write back with it, and a VM the panel does not hold in STARTING reads STOPPED after a start
// until the mirror catches up, so the patience with both has to outlast that with room to spare. n
// reads span n-1 waits, so the arithmetic is on the waits: counting reads is not counting time.
func TestRestartPatienceOutlastsThePanelsMirror(t *testing.T) {
	const need = 2 * time.Minute
	for _, tc := range []struct {
		what  string
		polls int
	}{
		{"a refusing panel", vmRestartMaxRefusals},
		{"a start that reads STOPPED", vmRestartMaxStoppedPolls},
	} {
		if patience := time.Duration(tc.polls-1) * vmRestartPollInterval; patience < need {
			t.Errorf("the restart gives up on %s after %v, which does not outlast the panel's mirror lag with room to spare (want at least %v)", tc.what, patience, need)
		}
	}
}

// The documentation and the changelog say that a stop or a start that keeps failing is repeated for
// about three minutes, and that a VM which reads STOPPED after a start has the same patience.
func TestRestartPatienceIsWhatTheDocsPromise(t *testing.T) {
	if patience := time.Duration(vmRestartMaxRefusals-1) * vmRestartPollInterval; patience < 170*time.Second || patience > 190*time.Second {
		t.Errorf("a failing call is repeated for %v, want about three minutes: the documentation says so", patience)
	}
	if vmRestartMaxStoppedPolls != vmRestartMaxRefusals {
		t.Errorf("a VM that reads STOPPED after a start has the patience of %d reads, want that of a refusal (%d): the documentation says so",
			vmRestartMaxStoppedPolls, vmRestartMaxRefusals)
	}
}

// The patience is counted in polls, so the poll interval is what turns it into time: a long one
// would stretch every wait, a short one would hammer the panel.
func TestRestartPollIntervalIsAFewSeconds(t *testing.T) {
	if vmRestartPollInterval < time.Second || vmRestartPollInterval > 10*time.Second {
		t.Errorf("poll interval = %v, want between 1s and 10s", vmRestartPollInterval)
	}
}

func TestRestartRunningVM_VMThatIsRunningAfterARefusedStartHasBeenRestarted(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.refuseCalls("start", 1)
	// The start is refused, and the VM is RUNNING when we look next: somebody else started it.
	p.setSteps([]string{"STOPPING", "STOPPED", "RUNNING"}, nil)

	cycled, err := p.restartWithin(t, c, 5*time.Second)
	if err != nil || !cycled {
		t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
	}
	wantCalls(t, p.posts(), []string{"stop", "start"})
}

func TestRestartRunningVM_StopThatHasNotTakenHoldIsWaitedFor(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	// The stop is accepted, but the VM reads RUNNING for a while longer: no reason to give up, and
	// none to start it.
	p.setSteps([]string{"RUNNING", "RUNNING", "STOPPING", "STOPPED"}, nil)

	cycled, err := p.restartWithin(t, c, 5*time.Second)
	if err != nil || !cycled {
		t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
	}
	wantCalls(t, p.posts(), []string{"stop", "start"})
	if _, atStart := p.statuses(); atStart != "STOPPED" {
		t.Errorf("started at %q, want STOPPED", atStart)
	}
}

func TestRestartRunningVM_RefusalsOfTheStopAndOfTheStartAreCountedApart(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.refuseCalls("stop", vmRestartMaxRefusals-1)
	p.refuseCalls("start", vmRestartMaxRefusals-1)
	// STOPPED right after the stop is accepted, with no STOPPING in between to settle the count.
	p.setSteps([]string{"STOPPED"}, nil)

	cycled, err := p.restartWithin(t, c, 5*time.Second)
	if err != nil || !cycled {
		t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
	}
	wantCalls(t, p.posts(), slices.Concat(callsOf("stop", vmRestartMaxRefusals), callsOf("start", vmRestartMaxRefusals)))
}

// A status the restart has no rule for, seen in the middle of a stop, is waited out: the VM has been
// touched, so giving up would leave it wherever it is, and the deadline bounds the wait. (A VM that has
// not been touched is another matter: see VMInAnUnusableStatusFailsAtOnce.)
func TestRestartRunningVM_AStatusItHasNoRuleForWhileStoppingIsWaitedOut(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.setSteps([]string{"BACKUP_RESTORE", "STOPPING", "STOPPED"}, nil)

	cycled, err := p.restartWithin(t, c, 5*time.Second)
	if err != nil || !cycled {
		t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
	}
	wantCalls(t, p.posts(), []string{"stop", "start"})
	if _, atStart := p.statuses(); atStart != "STOPPED" {
		t.Errorf("started at %q, want STOPPED", atStart)
	}
}

// The patience with the refusals of a stop is spent on the stop. A stop whose reply was lost and whose
// effect the panel's mirror shows late is refused over and over while the VM still reads RUNNING; once
// the VM shows that it took effect, a start that fails for a while has a patience of its own, not
// what is left of the stop's.
func TestRestartRunningVM_RefusalsOfALostStopDoNotEatIntoThePatienceOfTheStart(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	lag, failedStarts := vmRestartMaxRefusals-2, vmRestartMaxRefusals-2
	p.loseNextReplyShowingAfter("stop", lag)
	p.failCalls("start", failedStarts, badGateway)

	cycled, err := p.restartWithin(t, c, 5*time.Second)
	if err != nil || !cycled {
		t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
	}
	wantCalls(t, p.posts(), slices.Concat(callsOf("stop", lag+1), callsOf("start", failedStarts+1)))
}

// The same when the VM shows STOPPING, not STOPPED, after the refusals of the stop.
func TestRestartRunningVM_RefusalsOfAStopThatTheVMThenShowsDoNotEatIntoThePatienceOfTheStart(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	refused := vmRestartMaxRefusals - 2
	p.refuseCalls("stop", refused)
	p.refuseCalls("start", refused)
	// The stop is refused for as long as the VM reads RUNNING; then it reads STOPPING and STOPPED.
	p.setScript("RUNNING", append(slices.Repeat([]string{"RUNNING"}, refused), "STOPPING", "STOPPED")...)

	cycled, err := p.restartWithin(t, c, 5*time.Second)
	if err != nil || !cycled {
		t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
	}
	wantCalls(t, p.posts(), slices.Concat(callsOf("stop", refused), callsOf("start", refused+1)))
}

func TestRestartRunningVM_RefusalsAreForgottenWhenTheVMMoves(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	// Every run of refusals on the way is one fewer than it takes to give up, but there are more
	// of them than that in all: the VM goes back to STOPPING before the last.
	p.refuseCalls("start", vmRestartMaxRefusals)
	p.setSteps(slices.Concat([]string{"STOPPING"}, callsOf("STOPPED", vmRestartMaxRefusals-1), []string{"STOPPING", "STOPPED"}), nil)

	cycled, err := p.restartWithin(t, c, 5*time.Second)
	if err != nil || !cycled {
		t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
	}
	wantCalls(t, p.posts(), slices.Concat([]string{"stop"}, callsOf("start", vmRestartMaxRefusals+1)))
}

func TestRestartRunningVM_StartThatDoesNotTakeHoldFailsWithoutWaitingForTheDeadline(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.setSteps(nil, append([]string{"STARTING"}, slices.Repeat([]string{"STOPPED"}, vmRestartMaxStoppedPolls)...))

	_, err := p.restartWithin(t, c, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "STOPPED again after it was seen starting") || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the start that did not take hold, not a timeout", err)
	}
	wantCalls(t, p.posts(), []string{"stop", "start"})
}

// The limit is the number of reads of STOPPED in a row that ends the wait, not one more: a start that
// has read STOPPED that many times is called failed, though the next read would show RUNNING. One
// fewer is tolerated (see the test below), so the two pin the boundary.
func TestRestartRunningVM_AStartThatReadsStoppedTheLimitInARowDidNotTakeHold(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.setSteps(nil, append(slices.Repeat([]string{"STOPPED"}, vmRestartMaxStoppedPolls), "RUNNING"))

	cycled, err := p.restartWithin(t, c, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "the start did not take hold") {
		t.Fatalf("restartRunningVM = %v, %v; want the start that did not take hold", cycled, err)
	}
	wantCalls(t, p.posts(), []string{"stop", "start"})
}

func TestRestartRunningVM_ShortSpellsOfStoppedWhileStartingAreTolerated(t *testing.T) {
	stopped := []string{"STOPPED"}
	for _, tc := range []struct {
		name  string
		steps []string
	}{
		{"one fewer than the limit in a row", append(slices.Repeat(stopped, vmRestartMaxStoppedPolls-1), "STARTING", "RUNNING")},
		{"more than the limit, but never in a row", slices.Concat(
			slices.Repeat([]string{"STOPPED", "STARTING"}, vmRestartMaxStoppedPolls), []string{"RUNNING"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := newVMPanel(t, "RUNNING")
			p.setSteps(nil, tc.steps)

			cycled, err := p.restartWithin(t, c, 5*time.Second)
			if err != nil || !cycled {
				t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
			}
			wantCalls(t, p.posts(), []string{"stop", "start"})
		})
	}
}

// A VM the panel has been told to start can read STOPPED for over a minute (see vmRestartMaxStoppedPolls):
// the restart waits for it, as it does for a refusal, instead of calling a start that went through
// failed, which would taint the attachment and have the next apply stop a VM that is up.
func TestRestartRunningVM_AStartThatReadsStoppedForOverAMinuteIsWaitedFor(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	overAMinute := int(time.Minute/vmRestartPollInterval) + 2
	p.setSteps(nil, slices.Concat([]string{"STARTING"}, slices.Repeat([]string{"STOPPED"}, overAMinute), []string{"RUNNING"}))

	cycled, err := p.restartWithin(t, c, 5*time.Second)
	if err != nil || !cycled {
		t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
	}
	wantCalls(t, p.posts(), []string{"stop", "start"})
}

// A stop or a start can fail in a way that does not say whether the call was carried out: the panel's
// own 627, a 5xx from the panel or from the gateway in front of it (which times out, or restarts when
// the panel is deployed), a connection that is dropped. The restart judges such a failure by what the
// VM does next and goes on; an answer that is the panel's last word ends it.

func TestInconclusive(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"the panel's catch-all", &client.APIError{StatusCode: 500, Codes: []int{627}}, true},
		{"627 under another status", &client.APIError{StatusCode: 400, Codes: []int{627}}, true},
		{"a bare 500", &client.APIError{StatusCode: 500}, true},
		{"a bad gateway", &client.APIError{StatusCode: 502}, true},
		{"unavailable for want of compute", &client.APIError{StatusCode: 503, Codes: []int{744}}, true},
		{"a gateway timeout", &client.APIError{StatusCode: 504}, true},
		{"rate limited for good", &client.APIError{StatusCode: 429}, true},
		{"wrapped", fmt.Errorf("stopping: %w", &client.APIError{StatusCode: 502}), true},
		{"a dropped connection", errors.New(`request failed: Post "http://panel/stop": EOF`), true},
		{"forbidden", &client.APIError{StatusCode: 403, Codes: []int{403}}, false},
		{"unauthorized", &client.APIError{StatusCode: 401}, false},
		{"not found", &client.APIError{StatusCode: 404}, false},
		{"conflict", &client.APIError{StatusCode: 409}, false},
		{"a business error", &client.APIError{StatusCode: 400, Codes: []int{703}}, false},
		{"just below the 5xx", &client.APIError{StatusCode: 499}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := inconclusive(tc.err); got != tc.want {
				t.Errorf("inconclusive(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// answers are the ways a call can fail inconclusively, as the fake panel serves them.
var inconclusiveAnswers = []struct {
	name string
	a    answer
}{
	{"the panel's refusal", panelRefusal},
	{"gateway timeout", gatewayTimeout},
	{"bad gateway", badGateway},
	{"no capacity", noCompute},
	{"dropped connection", droppedAnswer},
}

func TestRestartRunningVM_StopThatFailsInconclusivelyIsRetried(t *testing.T) {
	for _, tc := range inconclusiveAnswers {
		t.Run(tc.name, func(t *testing.T) {
			c, p := newVMPanel(t, "RUNNING")
			p.failCalls("stop", 2, tc.a)

			cycled, err := p.restartWithin(t, c, 5*time.Second)
			if err != nil || !cycled {
				t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
			}
			// Two stops that failed before they were carried out, the one that went through, one start.
			wantCalls(t, p.posts(), []string{"stop", "stop", "stop", "start"})
		})
	}
}

func TestRestartRunningVM_StartThatFailsInconclusivelyIsRetried(t *testing.T) {
	for _, tc := range inconclusiveAnswers {
		t.Run(tc.name, func(t *testing.T) {
			c, p := newVMPanel(t, "RUNNING")
			p.failCalls("start", 2, tc.a)

			cycled, err := p.restartWithin(t, c, 5*time.Second)
			if err != nil || !cycled {
				t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
			}
			// The VM is off because of our stop: the start is asked for until it goes through, and the
			// stop is not repeated.
			wantCalls(t, p.posts(), []string{"stop", "start", "start", "start"})
		})
	}
}

func TestRestartRunningVM_StopWhoseReplyWasLostInAnyWayIsCarriedOn(t *testing.T) {
	for _, tc := range inconclusiveAnswers {
		t.Run(tc.name, func(t *testing.T) {
			c, p := newVMPanel(t, "RUNNING")
			p.loseNextReplyAs("stop", tc.a)

			cycled, err := p.restartWithin(t, c, 5*time.Second)
			if err != nil || !cycled {
				t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
			}
			// The panel stopped the VM and the reply never arrived: the VM is off because of our
			// stop, so it is started, once.
			wantCalls(t, p.posts(), []string{"stop", "start"})
			if _, atStart := p.statuses(); atStart != "STOPPED" {
				t.Errorf("started at %q, want STOPPED", atStart)
			}
		})
	}
}

func TestRestartRunningVM_StartWhoseReplyWasLostInAnyWayIsCarriedOn(t *testing.T) {
	for _, tc := range inconclusiveAnswers {
		t.Run(tc.name, func(t *testing.T) {
			c, p := newVMPanel(t, "RUNNING")
			p.loseNextReplyAs("start", tc.a)

			cycled, err := p.restartWithin(t, c, 5*time.Second)
			if err != nil || !cycled {
				t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
			}
			wantCalls(t, p.posts(), []string{"stop", "start"})
		})
	}
}

func TestRestartRunningVM_AFailureThatKeepsComingIsGivenUpOn(t *testing.T) {
	for _, op := range []string{"stop", "start"} {
		for _, tc := range inconclusiveAnswers {
			t.Run(op+"/"+tc.name, func(t *testing.T) {
				c, p := newVMPanel(t, "RUNNING")
				p.failCalls(op, -1, tc.a)

				_, err := p.restartWithin(t, c, 5*time.Second)
				if err == nil || !strings.Contains(err.Error(), "refusing to "+op+" VM 7") || errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("err = %v, want the panel's refusal to %s, not a timeout", err, op)
				}
				want := callsOf(op, vmRestartMaxRefusals)
				if op == "start" {
					want = slices.Concat([]string{"stop"}, want)
				}
				wantCalls(t, p.posts(), want)
			})
		}
	}
}

// Once a stop of ours may have taken effect the VM may be off, and a restart that gave up on it would
// leave it so: a status read that fails in a way that may pass gets the patience of a refusal.
func TestRestartRunningVM_ReadsThatFailAfterTheStopAreWaitedOut(t *testing.T) {
	// The first read finds the VM RUNNING and the stop goes through; the n that follow fail.
	afterTheStop := func(n int) []bool { return append([]bool{false}, slices.Repeat([]bool{true}, n)...) }

	t.Run("up to the patience minus one in a row", func(t *testing.T) {
		c, p := newVMPanel(t, "RUNNING")
		p.failReadsLike(afterTheStop(vmRestartMaxRefusals - 1)...)

		cycled, err := p.restartWithin(t, c, 5*time.Second)
		if err != nil || !cycled {
			t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
		}
		wantCalls(t, p.posts(), []string{"stop", "start"})
	})
	t.Run("the patience in a row is final", func(t *testing.T) {
		c, p := newVMPanel(t, "RUNNING")
		p.failReadsLike(afterTheStop(vmRestartMaxRefusals)...)

		_, err := p.restartWithin(t, c, 5*time.Second)
		if want := fmt.Sprintf("after %d consecutive failures", vmRestartMaxRefusals); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want the failed reads (%s)", err, want)
		}
		wantCalls(t, p.posts(), []string{"stop"})
	})
	t.Run("an answer that is final is not waited out", func(t *testing.T) {
		c, p := newVMPanel(t, "RUNNING")
		p.failReadsWith(answer{http.StatusForbidden, 403})
		p.failReadsLike(afterTheStop(vmRestartMaxPollErrs)...)

		_, err := p.restartWithin(t, c, 5*time.Second)
		if want := fmt.Sprintf("after %d consecutive failures", vmRestartMaxPollErrs); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want the failed reads (%s)", err, want)
		}
		wantCalls(t, p.posts(), []string{"stop"})
	})
	t.Run("also while a stop is refused", func(t *testing.T) {
		c, p := newVMPanel(t, "RUNNING")
		// The stop is refused, and the reads that follow fail: the refused stop may have been carried
		// out, so the VM may be off all the same.
		p.refuseCalls("stop", 1)
		p.failReadsLike(afterTheStop(vmRestartMaxPollErrs + 2)...)

		cycled, err := p.restartWithin(t, c, 5*time.Second)
		if err != nil || !cycled {
			t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
		}
		wantCalls(t, p.posts(), []string{"stop", "stop", "start"})
	})
}

// Reads that fail once the start was accepted are waited out as those after the stop are: the VM is
// coming up, and a restart that gave up now would taint the attachment of a VM that is running.
func TestRestartRunningVM_ReadsThatFailWhileTheVMIsStartingAreWaitedOut(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.setSteps([]string{"STOPPED"}, nil)
	p.failReadsWith(badGateway)
	// The first read finds the VM RUNNING and the stop goes through, the second finds it STOPPED and
	// the start goes through; then more reads fail in a row than a VM that has not been touched is given.
	p.failReadsLike(append([]bool{false, false}, slices.Repeat([]bool{true}, vmRestartMaxPollErrs+1)...)...)

	cycled, err := p.restartWithin(t, c, 5*time.Second)
	if err != nil || !cycled {
		t.Fatalf("restartRunningVM = %v, %v; want true, nil", cycled, err)
	}
	wantCalls(t, p.posts(), []string{"stop", "start"})
}

// The end of the wait is the end of the wait whatever read it finds in flight: a read that the context
// cut short is not one more failed read. Counted, it would reach the limit and be reported as the
// panel's failure, and it would be in the number of failed reads the timeout reports.
func TestRestartRunningVM_TheContextEndingDuringAReadIsNotAFailedRead(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	failed := vmRestartMaxPollErrs - 1
	p.failReadsLike(slices.Repeat([]bool{true}, failed)...)
	p.hangCalls("status") // the read after those hangs until the deadline cuts it short

	_, err := p.restartWithin(t, c, 100*time.Millisecond)
	want := fmt.Sprintf("timed out waiting for VM 7 to settle (last status unknown; the last %d status reads failed", failed)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want a timeout that counts the %d reads that failed (%q)", err, failed, want)
	}
}

// A deadline that ends the wait while the panel cannot be read says so: "last status RUNNING" alone
// would not tell why the restart did not get on.
func TestRestartRunningVM_TimeoutSaysThatTheReadsFailed(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.failReadsLike(append([]bool{false}, slices.Repeat([]bool{true}, 1000)...)...)

	// The patience is 36 polls here, far more than the deadline allows.
	_, err := p.restartWithinPoll(t, c, 150*time.Millisecond, 20*time.Millisecond)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) ||
		!strings.Contains(err.Error(), "timed out waiting for VM 7 to stop") ||
		!strings.Contains(err.Error(), "status reads failed, the latest with: api error [627] (http 500)") {
		t.Fatalf("err = %v, want a timeout that says the status reads failed, and how", err)
	}
}

func TestRestartRunningVM_StartThatKeepsBeingRefusedTimesOutWaitingToStart(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.refuseCalls("start", -1)

	// The patience is 36 polls here, far more than the deadline allows.
	_, err := p.restartWithinPoll(t, c, 150*time.Millisecond, 20*time.Millisecond)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) ||
		!strings.Contains(err.Error(), "timed out waiting for VM 7 to start") || !strings.Contains(err.Error(), "last status STOPPED") {
		t.Fatalf("err = %v, want a timeout waiting for the VM to start", err)
	}
}

// The end of the wait is the end of the wait whatever call it finds in flight: the failure of a call that
// the context cut short must not be reported as the panel's.
func TestRestartRunningVM_TheContextEndingDuringACallIsNotThePanelsFailure(t *testing.T) {
	for _, tc := range []struct{ op, waiting string }{{"stop", "settle"}, {"start", "start"}} {
		t.Run(tc.op, func(t *testing.T) {
			t.Run("cancelled", func(t *testing.T) {
				c, p := newVMPanel(t, "RUNNING")
				p.hangCalls(tc.op)

				ctx, cancel := context.WithCancel(context.Background())
				time.AfterFunc(100*time.Millisecond, cancel)
				done := make(chan error, 1)
				go func() {
					_, err := restartRunningVM(ctx, c, vmPanelVMID, &client.RequestOpts{}, restartTestPoll)
					done <- err
				}()

				select {
				case err := <-done:
					if !errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
						!strings.Contains(err.Error(), "interrupted waiting for VM 7 to "+tc.waiting) ||
						strings.Contains(err.Error(), "request failed") {
						t.Fatalf("err = %v, want an interruption waiting for the VM to %s", err, tc.waiting)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("restartRunningVM ignored the cancellation")
				}
			})
			t.Run("deadline", func(t *testing.T) {
				c, p := newVMPanel(t, "RUNNING")
				p.hangCalls(tc.op)

				_, err := p.restartWithin(t, c, 100*time.Millisecond)
				if err == nil || !errors.Is(err, context.DeadlineExceeded) ||
					!strings.Contains(err.Error(), "timed out waiting for VM 7 to "+tc.waiting) ||
					strings.Contains(err.Error(), "request failed") {
					t.Fatalf("err = %v, want a timeout waiting for the VM to %s", err, tc.waiting)
				}
			})
		})
	}
}

func TestVMRestartWaitingFor(t *testing.T) {
	for _, tc := range []struct {
		phase      vmRestartPhase
		sawStopped bool
		want       string
	}{
		{restartLooking, false, "waiting for VM 7 to settle"},
		{restartStopping, false, "waiting for VM 7 to stop"},
		{restartStopping, true, "waiting for VM 7 to start"},
		{restartStarting, false, "waiting for VM 7 to start"},
		{restartStarting, true, "waiting for VM 7 to start"},
	} {
		if got := vmRestartWaitingFor(tc.phase, tc.sawStopped, 7); got != tc.want {
			t.Errorf("vmRestartWaitingFor(%v, %v) = %q, want %q", tc.phase, tc.sawStopped, got, tc.want)
		}
	}
}
