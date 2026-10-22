package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// movePanel is vmPanel with the calls an update makes besides the power cycle: the rename, the
// resize and the move of the main network (PUT .../primary-local-network, and its dryRun). The VM
// is the one of vmState(): the status reads report what the panel has of it.
type movePanel struct {
	*vmPanel

	// The VM as the panel has it; the lock of the vmPanel guards all of it.
	name         string
	networkID    int64 // the panel's record: what the status reads report
	nicNetworkID int64 // where the interface really is, when the record is off; 0 for the record
	privateIP    string
	cpu, ram     int64
	diskSize     int64
	diskType     string
	noCloudInit  bool

	// dryRuns and moves are replies the panel gives to the next checks and moves, in order; when
	// they run out it answers as the real one would.
	dryRuns, moves []moveReply
	// failing maps "rename", "resources" and "disk" to the answer the call gets instead of being
	// carried out. These calls can be held until the client gives up on them as well (hangCalls);
	// onHeld is then called when one arrives, before it is held.
	failing map[string]answer
	onHeld  func(op string)
	// reportedNetwork, when not 0, is the network the status reads report whatever the VM's: a
	// panel that disagrees with itself.
	reportedNetwork int64
	// realIP, when set, is the address a move (not a check) gives when none is asked for: the one
	// the check forecast was taken by another VM in the minutes between the two.
	realIP string

	statusAt   map[string]string // the VM's status when each call ("rename", "resources", "disk", "move") arrived
	bodies     []moveBody        // what each PUT asked for
	diskBodies []map[string]any  // the body of each PATCH of the disk the panel carried out

	// onMove, when set, is called when a move (not a check) arrives, before the panel carries it
	// out; the move is carried out and answered after it returns. A test makes the reply lost by
	// having it wait until the client has given up.
	onMove func(r *http.Request)
}

// moveReply is a scripted answer to a check or a move of the network.
type moveReply struct {
	fail *answer // the failure to answer with, whatever the move would have answered
	// applyFirst carries the move out before it fails: a reply that is lost.
	applyFirst bool
	// pending makes the success answer say that an earlier move was settled first.
	pending bool
	// noForecast makes a check answer the way the panel does when the VM has an unsettled move:
	// no forecast, only pendingRecovery.
	noForecast bool
	// alsoCode, when not 0, is a second code the failure names in the same answer.
	alsoCode int
}

// moveBody is what a PUT to primary-local-network asked for.
type moveBody struct {
	dryRun    bool
	networkID int64
	ip        string
}

// moveFails is a reply that refuses with a panel error.
func moveFails(status, code int) moveReply { return moveReply{fail: &answer{status, code}} }

func newMovePanel(t *testing.T, status string) (*client.Client, *movePanel) {
	t.Helper()
	c, p := newVMPanel(t, status)
	m := &movePanel{
		vmPanel:   p,
		name:      "web-1",
		networkID: vmNetOld,
		privateIP: "192.168.0.5",
		cpu:       2,
		ram:       4,
		diskSize:  20,
		diskType:  "SSD",
		failing:   map[string]answer{},
		statusAt:  map[string]string{},
	}
	p.extra = m.serve
	p.bodyExtra = m.fill
	return c, m
}

func (m *movePanel) fill(body map[string]any) {
	network := m.networkID
	if m.reportedNetwork != 0 {
		network = m.reportedNetwork
	}
	body["name"] = m.name
	body["cpuCores"], body["ram"] = m.cpu, m.ram
	body["diskSize"], body["diskType"] = m.diskSize, m.diskType
	body["localNetworkId"], body["privateIp"] = network, m.privateIP
	body["imageId"], body["imageName"], body["imageSlug"] = 3, "Ubuntu 24.04", "ubuntu-24.04"
}

// serve answers what the vmPanel does not know; it runs with the panel's lock held.
func (m *movePanel) serve(w http.ResponseWriter, r *http.Request) bool {
	op := ""
	switch {
	case r.Method == http.MethodPatch && r.URL.Path == vmPanelBase+"/name":
		op = "rename"
	case r.Method == http.MethodPatch && r.URL.Path == vmPanelBase+"/resources":
		op = "resources"
	case r.Method == http.MethodPatch && r.URL.Path == vmPanelBase+"/disk":
		op = "disk"
	case r.Method == http.MethodPut && r.URL.Path == vmPanelBase+"/primary-local-network":
		m.handleMove(w, r)
		return true
	default:
		return false
	}

	m.statusAt[op] = m.status
	// The body is read before anything else: the server notices that the client gave up on a call it
	// holds only once the body has been read to its end.
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	_, _ = io.Copy(io.Discard, r.Body)
	if m.hanging[op] && m.onHeld != nil {
		m.onHeld(op)
	}
	if m.hold(r, op) {
		return true
	}
	if a, ok := m.failing[op]; ok {
		m.failWith(w, a)
		return true
	}
	switch op {
	case "rename":
		m.name, _ = body["name"].(string)
	case "resources":
		cpu, _ := body["cpuCores"].(float64)
		ram, _ := body["ram"].(float64)
		m.cpu, m.ram = int64(cpu), int64(ram)
	case "disk":
		m.diskBodies = append(m.diskBodies, body)
		if v, ok := body["diskSize"].(float64); ok {
			m.diskSize = int64(v)
		}
		if v, ok := body["diskType"].(string); ok {
			m.diskType = v
		}
	}
	m.ok(w, nil)
	return true
}

func (m *movePanel) nic() int64 {
	if m.nicNetworkID != 0 {
		return m.nicNetworkID
	}
	return m.networkID
}

// handleMove answers a check or a move: the next scripted reply, or what the real panel does.
func (m *movePanel) handleMove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NetworkID int64  `json:"networkId"`
		IP        string `json:"ip"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	dryRun := r.URL.Query().Get("dryRun") == "true"
	m.bodies = append(m.bodies, moveBody{dryRun: dryRun, networkID: body.NetworkID, ip: body.IP})
	if !dryRun {
		m.statusAt["move"] = m.status
		if m.onMove != nil {
			m.onMove(r)
		}
	}

	var reply moveReply
	queue := &m.moves
	if dryRun {
		queue = &m.dryRuns
	}
	if len(*queue) > 0 {
		reply, *queue = (*queue)[0], (*queue)[1:]
	}

	changed := body.NetworkID != m.nic()
	ip := body.IP
	if ip == "" {
		ip = fmt.Sprintf("192.168.%d.50", body.NetworkID)
		if !dryRun && m.realIP != "" {
			ip = m.realIP
		}
	}
	previous := m.nic()
	apply := func() {
		if !dryRun {
			m.networkID, m.nicNetworkID, m.privateIP = body.NetworkID, 0, ip
		}
	}

	if reply.fail != nil {
		if reply.applyFirst && changed {
			apply()
		}
		if reply.alsoCode != 0 {
			m.failWithCodes(w, reply.fail.status, reply.fail.code, reply.alsoCode)
			return
		}
		m.failWith(w, *reply.fail)
		return
	}

	// An unsettled move of the VM: the panel forecasts nothing.
	if reply.noForecast && dryRun {
		m.ok(w, map[string]any{
			"networkId": body.NetworkID, "changed": false, "gatewayMoved": false,
			"requiresGuestConfiguration": false, "pendingRecovery": true,
		})
		return
	}

	// The real panel: a VM that is in the network already keeps its address, and another one is not
	// what the move does (967). The answer is about the interface as it is.
	if !changed {
		if body.IP != "" && body.IP != m.privateIP {
			m.failWith(w, answer{http.StatusConflict, 967})
			return
		}
		ip = m.privateIP
	}

	// The real panel refuses a move of a VM that runs, and a check does not ask about it.
	if changed && !dryRun && m.status != "STOPPED" {
		m.failWith(w, answer{http.StatusConflict, 963})
		return
	}
	if changed || body.NetworkID != m.networkID {
		apply()
	}

	// requiresGuestConfiguration is about the VM, not about this call: the panel says so for the
	// answer of a move that was made before, too.
	answer := map[string]any{
		"networkId":                  body.NetworkID,
		"networkName":                "net-b",
		"ip":                         ip,
		"gateway":                    "192.168.1.1",
		"changed":                    changed,
		"gatewayMoved":               changed,
		"requiresGuestConfiguration": m.noCloudInit,
		"pendingRecovery":            reply.pending,
	}
	if changed {
		answer["previousNetworkId"] = previous
	}
	if !dryRun {
		answer["id"], answer["mac"] = 99, "bc:24:11:00:00:01"
	}
	m.ok(w, answer)
}

// calls is what the panel was asked, in order, without the status reads: "check" and "move" for
// the two kinds of PUT to primary-local-network, "rename", "resources", "disk", "stop", "start".
func (m *movePanel) calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for i, request := range m.requests {
		switch request {
		case "PUT /api/v2/vms/7/primary-local-network":
			if strings.Contains(m.queries[i], "dryRun=true") {
				out = append(out, "check")
			} else {
				out = append(out, "move")
			}
		case "PATCH /api/v2/vms/7/name":
			out = append(out, "rename")
		case "PATCH /api/v2/vms/7/resources":
			out = append(out, "resources")
		case "PATCH /api/v2/vms/7/disk":
			out = append(out, "disk")
		case "POST /api/v2/vms/7/stop":
			out = append(out, "stop")
		case "POST /api/v2/vms/7/start":
			out = append(out, "start")
		}
	}
	return out
}

func (m *movePanel) scriptMoves(dry, moves []moveReply) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dryRuns, m.moves = dry, moves
}

func (m *movePanel) failCall(op string, a answer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failing[op] = a
}

func (m *movePanel) setNetworkRecord(record, nic int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.networkID, m.nicNetworkID = record, nic
}

// setAddress sets the address the VM has now, whatever the network the panel records it in.
func (m *movePanel) setAddress(ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.privateIP = ip
}

// setRealIP makes a move that asks for no address get ip, while a check forecasts the usual one.
func (m *movePanel) setRealIP(ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.realIP = ip
}

func (m *movePanel) setNoCloudInit() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.noCloudInit = true
}

func (m *movePanel) setReportedNetwork(id int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reportedNetwork = id
}

// vm is what the panel has of the VM.
func (m *movePanel) vm() (network int64, ip, name string, cpu, ram, disk int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.networkID, m.privateIP, m.name, m.cpu, m.ram, m.diskSize
}

func (m *movePanel) statusWhen(op string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusAt[op]
}

func (m *movePanel) moveBodies() []moveBody {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]moveBody(nil), m.bodies...)
}

// diskRequests are the bodies of the PATCHes of the disk the panel carried out, as it decoded them.
func (m *movePanel) diskRequests() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]map[string]any(nil), m.diskBodies...)
}

// failWithCodes refuses with an answer that names several codes at once.
func (m *movePanel) failWithCodes(w http.ResponseWriter, status int, codes ...int) {
	errs := make([]map[string]any, 0, len(codes))
	for _, code := range codes {
		errs = append(errs, map[string]any{"code": code, "message": "refused"})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "data": nil, "errors": errs})
}

// afterBodyClosed is a transport that calls hook once the answer to a request that matches has been
// read and closed: the one moment between two calls of the client that a test can pick without a
// race with it.
type afterBodyClosed struct {
	http.RoundTripper
	match func(*http.Request) bool
	hook  func()
}

func (a afterBodyClosed) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := a.RoundTripper.RoundTrip(r)
	if err == nil && a.match(r) {
		resp.Body = closeHook{ReadCloser: resp.Body, hook: a.hook}
	}
	return resp, err
}

type closeHook struct {
	io.ReadCloser
	hook func()
}

func (c closeHook) Close() error {
	err := c.ReadCloser.Close()
	c.hook()
	return err
}

// useTransport makes the clients of the test, which have no transport of their own, go through
// wrap(the default one) until the test ends.
func useTransport(t *testing.T, wrap func(http.RoundTripper) http.RoundTripper) {
	t.Helper()
	previous := http.DefaultTransport
	http.DefaultTransport = wrap(previous)
	t.Cleanup(func() { http.DefaultTransport = previous })
}

// updateHarness runs Update of prodata_vm against a movePanel, with the plan Terraform would have
// made.
type updateHarness struct {
	t        *testing.T
	planner  *vmPlanner
	resource *VmResource
	panel    *movePanel
	// ctx is what Update is called with; a test replaces it to interrupt the update.
	ctx context.Context
}

// quickMovePause makes the retries of a move that has no definite answer come at once.
func quickMovePause(int) time.Duration { return time.Millisecond }

func newUpdateHarness(t *testing.T, status string) *updateHarness {
	t.Helper()
	c, m := newMovePanel(t, status)
	return &updateHarness{
		t:       t,
		planner: newVMPlanner(t),
		resource: &VmResource{
			client:      c,
			poll:        restartTestPoll,
			moveOptions: []client.MoveOption{client.WithMovePause(quickMovePause)},
		},
		panel: m,
		ctx:   context.Background(),
	}
}

// updateResult is what Update left: the state it wrote (the prior one when it failed) and what it
// told the user.
type updateResult struct {
	state      VmResourceModel
	wrote      bool // the state differs from the prior one
	fullyKnown bool // the state has no unknown value in it: Terraform rejects one that has
	errs       []string
	warnings   []string
}

func (r updateResult) failed() bool { return len(r.errs) > 0 }

// wantOK fails the test unless Update succeeded without a warning.
func (r updateResult) wantOK(t *testing.T) {
	t.Helper()
	if r.failed() {
		t.Fatalf("Update failed: %q", r.errs)
	}
	if len(r.warnings) > 0 {
		t.Errorf("Update warned: %q", r.warnings)
	}
}

// wantFailed fails the test unless Update failed with errors that between them hold every part, and
// left the state as it was: a failed apply keeps the state it started from.
func (r updateResult) wantFailed(t *testing.T, parts ...string) {
	t.Helper()
	if !r.failed() {
		t.Fatalf("Update succeeded, want an error with %q", parts)
	}
	for _, part := range parts {
		if !contains(r.errs, part) {
			t.Errorf("errors = %q, want one with %q", r.errs, part)
		}
	}
	if r.wrote {
		t.Errorf("Update changed the state and failed: it has to leave the state it started from")
	}
}

// wantNetwork fails the test unless the state has the VM in the network, at the address.
func (r updateResult) wantNetwork(t *testing.T, id int64, ip string) {
	t.Helper()
	if got := r.state.LocalNetworkID.ValueInt64(); got != id {
		t.Errorf("local_network_id in the state = %d, want %d", got, id)
	}
	if got := r.state.PrivateIP.ValueString(); got != ip {
		t.Errorf("private_ip in the state = %q, want %q", got, ip)
	}
}

// summaries are the headlines of the diagnostics.
func summaries(diags diag.Diagnostics) (errs, warnings []string) {
	for _, d := range diags {
		text := d.Summary() + ": " + d.Detail()
		if d.Severity() == diag.SeverityError {
			errs = append(errs, text)
		} else {
			warnings = append(warnings, text)
		}
	}
	return errs, warnings
}

// apply plans the change from prior to config through the framework, and runs Update on the plan.
func (h *updateHarness) apply(prior, config vmValues) updateResult {
	h.t.Helper()
	planned := h.planner.plan(prior, config)
	if len(planned.errors) > 0 {
		h.t.Fatalf("the plan failed: %v", planned.errors)
	}
	if len(planned.requiresReplace) > 0 {
		h.t.Fatalf("the plan replaces the VM (%v): Update is not called for it", planned.requiresReplace)
	}
	return h.update(prior, planned.planned)
}

// update runs Update on a prior state and a plan.
func (h *updateHarness) update(prior, planned vmValues) updateResult {
	h.t.Helper()
	s := vmSchema(h.t)
	priorState := tfsdk.State{Schema: s, Raw: h.planner.object(prior)}
	req := resource.UpdateRequest{
		State: priorState,
		Plan:  tfsdk.Plan{Schema: s, Raw: h.planner.object(planned)},
	}
	resp := resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: priorState.Raw.Copy()}}

	h.resource.Update(h.ctx, req, &resp)

	var out updateResult
	out.errs, out.warnings = summaries(resp.Diagnostics)
	out.wrote = !resp.State.Raw.Equal(priorState.Raw)
	out.fullyKnown = resp.State.Raw.IsFullyKnown()
	if diags := resp.State.Get(context.Background(), &out.state); diags.HasError() {
		h.t.Fatalf("reading the state Update left: %v", diags)
	}
	if !out.failed() {
		h.checkResult(planned, resp.State.Raw)
	}
	return out
}

// checkResult is what Terraform makes of the state an apply leaves: no unknown value in it, and
// every value the plan knew stored as planned. Anything else is "Provider produced inconsistent
// result after apply", which says nothing of what was done to the VM.
func (h *updateHarness) checkResult(planned vmValues, state tftypes.Value) {
	h.t.Helper()
	if !state.IsFullyKnown() {
		h.t.Errorf("the state Update left has unknown values in it: %v", state)
	}
	var got map[string]tftypes.Value
	if err := state.As(&got); err != nil {
		h.t.Fatalf("reading the state Update left: %v", err)
	}
	for name, want := range planned {
		if !want.IsFullyKnown() {
			continue
		}
		if have, ok := got[name]; !ok || !have.Equal(want) {
			h.t.Errorf("inconsistent result after apply: %s is planned as %v and stored as %v", name, want, have)
		}
	}
}

func contains(texts []string, part string) bool {
	for _, t := range texts {
		if strings.Contains(t, part) {
			return true
		}
	}
	return false
}
