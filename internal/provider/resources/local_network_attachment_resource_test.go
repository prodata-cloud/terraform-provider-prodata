package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Tests for prodata_local_network_attachment against a fake panel: what Create, Read, Update,
// Delete and ImportState send, what they record, and what they tell the user.

const (
	lnaNetworkID   = 412
	lnaAddress     = "192.168.20.15"
	lnaInterfaceID = 9071
	lnaMAC         = "BC:24:11:AA:BB:CC"
	lnaNICBase     = vmPanelBase + "/local-networks"

	lnaSummaryFailed       = "Unable to Attach Local Network"
	lnaSummaryRestartFails = "Local Network Attached, but the VM Could Not Be Restarted"
	lnaSummaryNotRestarted = "Local Network Attached, but the VM Was Not Restarted"
	lnaSummaryRestartNeeds = "Local Network Attached, but It May Work Only After the VM Restarts"
	lnaSummaryGuest        = "Local Network Attached, but the Address Must Be Set Inside the VM"
	lnaSummaryDrift        = "Local Network Interface Address Changed Outside Terraform"

	// lnaMoveGateway is the advice on the gateway as the resource words it, written out here in full:
	// a test that quoted the constants of the resource would not notice a change to them. It ends a sentence.
	lnaMoveGateway = "move the gateway to the interface that is to carry the VM's default route, and leave this one without it. " +
		"The console can do that only for a VM without a public IP, and its No gateway option clears the gateway of every interface of the VM, " +
		"not only of the one it is chosen on. The steps are in the documentation of this resource, in the section \"Destroying the attachment\"."

	// lnaSecondRestart is the third cause in the text for a platform error on detach, written out here in full: a
	// test that only looked for phrases would not notice a condition that has been widened or a reason that is gone.
	lnaSecondRestart = "- If the destroy fails again after that, the interface may only have been waiting for a restart to appear: it was attached, " +
		"without reboot_after_attach, to a VM that does not hot-plug network devices, and the VM had not been restarted since. " +
		"The restart above then brought it up, and its removal is queued in turn: stop and start the VM once more, then run " +
		"terraform destroy again. If that does not describe the VM, go on to the next step.\n"

	// lnaCeiling bounds every call the tests make, whatever the resource does: a resource that
	// waits for a VM that never settles, or ignores its timeout, would otherwise hang until the
	// test binary's deadline instead of failing the test that caught it. No correct call comes
	// near it: the tests poll every millisecond.
	lnaCeiling = 10 * time.Second
)

// nicPanel is a minimal panel-main for the local network interfaces of the VM vmPanel serves (id
// 7): the routes under /api/v2/vms/7/local-networks, with vmPanel answering for the VM's status and
// its stop and start. Any request it does not know is a test failure.
type nicPanel struct {
	vp  *vmPanel
	mu  sync.Mutex
	srv *httptest.Server

	nics   []client.VmLocalNetwork // what the list reports
	nextID int64                   // the id of the next interface attached
	nextIP int                     // the last octet handed out when no address is asked for

	noCloudInit      bool // an attach reports requiresGuestConfiguration
	noMAC            bool // an attach reports no MAC address
	stopsAfterAttach bool // the VM goes down right after an attach, as if somebody stopped it
	holdStatusFrom   int  // the nth status read and every later one hang until the client gives up (0: never)
	statusReads      atomic.Int64
	holdAttachFrom   int // the nth attach and every later one hang until the client gives up (0: never)
	attachRequests   atomic.Int64
	onHold           func() // called when an attach is held, once it has been read
	holdListFrom     int    // the nth list and every later one hang until the client gives up (0: never)
	listRequests     atomic.Int64
	vmProject        string // the project the VM lives in; a request in another one is answered 633 (empty: any)

	queued map[string][]answer // op -> answers for the next calls, one each, before calls succeed
	always map[string]answer   // op -> the answer of every call that no queued answer covers

	calls   []string // "list", "attach", "detach <id>" in arrival order
	queries []string // the query string of each call in calls
	bodies  []string // the body of each attach
}

func newNICPanel(t *testing.T, vmStatus string) (*client.Client, *nicPanel) {
	t.Helper()
	_, vp := newVMPanel(t, vmStatus)
	p := &nicPanel{
		vp:     vp,
		nextID: lnaInterfaceID,
		nextIP: 2,
		queued: map[string][]answer{},
		always: map[string]answer{},
	}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p.holdAttachFrom > 0 && r.Method == http.MethodPost && r.URL.Path == lnaNICBase && int(p.attachRequests.Add(1)) >= p.holdAttachFrom {
			p.mu.Lock()
			p.record("attach", r)
			p.mu.Unlock()
			// The body is read first: the server notices a client that gave up only after it has.
			readBody(r)
			if p.onHold != nil {
				p.onHold()
			}
			<-r.Context().Done()
			return
		}
		if p.holdListFrom > 0 && r.Method == http.MethodGet && r.URL.Path == lnaNICBase && int(p.listRequests.Add(1)) >= p.holdListFrom {
			p.mu.Lock()
			p.record("list", r)
			p.mu.Unlock()
			<-r.Context().Done()
			return
		}
		if strings.HasPrefix(r.URL.Path, lnaNICBase) {
			p.serve(t, w, r)
			return
		}
		if p.holdStatusFrom > 0 && strings.HasSuffix(r.URL.Path, "/status") && int(p.statusReads.Add(1)) >= p.holdStatusFrom {
			<-r.Context().Done()
			return
		}
		vp.serve(t, w, r)
	}))
	t.Cleanup(p.srv.Close)

	c, err := client.New(client.Config{APIBaseURL: p.srv.URL, APIKeyID: "k", APISecretKey: "s", Region: "TEST", ProjectTag: "proj"})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	return c, p
}

func (p *nicPanel) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && r.URL.Path == lnaNICBase:
		p.record("list", r)
		if p.outOfProject(w, r) {
			return
		}
		if a, ok := p.failure("list"); ok {
			p.vp.failWith(w, a)
			return
		}
		p.vp.ok(w, p.nics)

	case r.Method == http.MethodPost && r.URL.Path == lnaNICBase:
		p.record("attach", r)
		b := new(strings.Builder)
		if _, err := fmt.Fprint(b, readBody(r)); err != nil {
			t.Errorf("attach body: %v", err)
		}
		p.bodies = append(p.bodies, b.String())
		if p.outOfProject(w, r) {
			return
		}
		if a, ok := p.failure("attach"); ok {
			p.vp.failWith(w, a)
			return
		}
		var req client.AttachLocalNetworkRequest
		if err := json.Unmarshal([]byte(b.String()), &req); err != nil {
			t.Errorf("attach body: %v", err)
		}
		running := p.vp.current() == "RUNNING"
		ip := req.IP
		if ip == "" {
			ip = fmt.Sprintf("192.168.20.%d", p.nextIP)
			p.nextIP++
		}
		nic := client.VmLocalNetwork{ID: p.nextID, NetworkID: &req.NetworkID, NetworkName: ptr("backend"), IP: &ip}
		if !p.noMAC {
			nic.MAC = ptr(lnaMAC)
		}
		p.nextID++
		p.nics = append(p.nics, nic)
		if p.stopsAfterAttach {
			p.vp.setScript("STOPPED")
		}
		p.vp.ok(w, map[string]any{
			"id": nic.ID, "networkId": nic.NetworkID, "networkName": nic.NetworkName, "ip": nic.IP, "mac": nic.MAC,
			"gateway": nil, "primary": false,
			"requiresRestart": running, "requiresGuestConfiguration": p.noCloudInit,
		})

	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, lnaNICBase+"/"):
		id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, lnaNICBase+"/"), 10, 64)
		if err != nil {
			t.Errorf("detach path %s: %v", r.URL.Path, err)
			http.NotFound(w, r)
			return
		}
		p.record(fmt.Sprintf("detach %d", id), r)
		if p.outOfProject(w, r) {
			return
		}
		if a, ok := p.failure("detach"); ok {
			p.vp.failWith(w, a)
			return
		}
		i := slices.IndexFunc(p.nics, func(n client.VmLocalNetwork) bool { return n.ID == id })
		if i < 0 {
			p.vp.failWith(w, answer{http.StatusNotFound, 703})
			return
		}
		p.nics = slices.Delete(p.nics, i, i+1)
		p.vp.ok(w, nil)

	default:
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}
}

// outOfProject answers 633, as the panel does for a VM that is not in the caller's project, when the
// request asks in a project other than the one the VM lives in; the query parameter wins over the
// header, as it does there.
func (p *nicPanel) outOfProject(w http.ResponseWriter, r *http.Request) bool {
	if p.vmProject == "" {
		return false
	}
	project := r.URL.Query().Get("projectTag")
	if project == "" {
		project = r.Header.Get("X-Project-Tag")
	}
	if project == p.vmProject {
		return false
	}
	p.vp.failWith(w, answer{http.StatusNotFound, 633})
	return true
}

func (p *nicPanel) record(call string, r *http.Request) {
	p.calls = append(p.calls, call)
	p.queries = append(p.queries, r.URL.RawQuery)
}

func (p *nicPanel) failure(op string) (answer, bool) {
	if q := p.queued[op]; len(q) > 0 {
		p.queued[op] = q[1:]
		return q[0], true
	}
	a, ok := p.always[op]
	return a, ok
}

func readBody(r *http.Request) string {
	var sb strings.Builder
	buf := make([]byte, 512)
	for {
		n, err := r.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			return sb.String()
		}
	}
}

func ptr[T any](v T) *T { return &v }

// failNext makes the next calls of op (list, attach or detach) fail with these answers, one each.
func (p *nicPanel) failNext(op string, answers ...answer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queued[op] = answers
}

// failEvery makes every call of op fail with a, once the queued answers are used up.
func (p *nicPanel) failEvery(op string, a answer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.always[op] = a
}

// inProject puts the VM in a project other than the provider's.
func (p *nicPanel) inProject(project string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.vmProject = project
}

// has puts an interface on the VM, as if it had been attached before.
func (p *nicPanel) has(nic client.VmLocalNetwork) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nics = append(p.nics, nic)
}

func (p *nicPanel) nicCalls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.calls)
}

func (p *nicPanel) nicQueries() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.queries)
}

func (p *nicPanel) attachBodies() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.bodies)
}

func (p *nicPanel) listed() []client.VmLocalNetwork {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.nics)
}

// stored is an interface the resource has recorded: in network 412, with the address and MAC the
// tests use throughout.
func stored() client.VmLocalNetwork {
	return client.VmLocalNetwork{ID: lnaInterfaceID, NetworkID: ptr(int64(lnaNetworkID)), NetworkName: ptr("backend"), IP: ptr(lnaAddress), MAC: ptr(lnaMAC)}
}

// mainNIC is the VM's own interface, in the network the VM was created in. The list of a real panel
// has it first, so a lookup of the interface in a network has to tell it from the one it looks for.
func mainNIC() client.VmLocalNetwork {
	return client.VmLocalNetwork{ID: 9070, NetworkID: ptr(int64(411)), NetworkName: ptr("main"), IP: ptr("192.168.10.5"), MAC: ptr("BC:24:11:00:00:01"), Primary: true}
}

// ---- state and plan builders ----

func lnaSchema(t *testing.T) (tftypes.Object, schema.Schema) {
	t.Helper()
	var resp resource.SchemaResponse
	NewLocalNetworkAttachmentResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	obj, ok := resp.Schema.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("schema type is not an object")
	}
	return obj, resp.Schema
}

// lnaRaw builds an object value with every attribute null except those in set.
func lnaRaw(t *testing.T, set map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	obj, _ := lnaSchema(t)
	vals := make(map[string]tftypes.Value, len(obj.AttributeTypes))
	for k, typ := range obj.AttributeTypes {
		vals[k] = tftypes.NewValue(typ, nil)
	}
	for k, v := range set {
		if _, ok := vals[k]; !ok {
			t.Fatalf("unknown attribute %q", k)
		}
		vals[k] = v
	}
	return tftypes.NewValue(obj, vals)
}

// lnaTimeouts is a timeouts block with these values set, or the null block when both are "".
func lnaTimeouts(t *testing.T, create, del string) tftypes.Value {
	t.Helper()
	obj, _ := lnaSchema(t)
	typ, ok := obj.AttributeTypes["timeouts"].(tftypes.Object)
	if !ok {
		t.Fatal("timeouts is not an object attribute")
	}
	if create == "" && del == "" {
		return tftypes.NewValue(typ, nil)
	}
	vals := make(map[string]tftypes.Value, len(typ.AttributeTypes))
	for k, at := range typ.AttributeTypes {
		vals[k] = tftypes.NewValue(at, nil)
	}
	if create != "" {
		vals["create"] = nirStr(create)
	}
	if del != "" {
		vals["delete"] = nirStr(del)
	}
	return tftypes.NewValue(typ, vals)
}

// lnaFlag is a reboot_after_attach value: "true", "false", or anything else for null.
func lnaFlag(v string) tftypes.Value { return pipFlag(v) }

// lnaCreatePlan is the plan Terraform hands Create: what the user set, and unknown for what the
// provider fills in. override replaces attributes; a null Value removes one.
func lnaCreatePlan(t *testing.T, override map[string]tftypes.Value) tfsdk.Plan {
	t.Helper()
	_, sch := lnaSchema(t)
	set := map[string]tftypes.Value{
		"vm_id":               nirNum(vmPanelVMID),
		"network_id":          nirNum(lnaNetworkID),
		"ip":                  nirStr(lnaAddress),
		"interface_id":        nirNumUnknown(),
		"mac_address":         nirStrUnknown(),
		"region":              nirStrUnknown(),
		"project_tag":         nirStrUnknown(),
		"reboot_after_attach": lnaFlag(""),
		"timeouts":            lnaTimeouts(t, "", ""),
	}
	for k, v := range override {
		set[k] = v
	}
	return tfsdk.Plan{Schema: sch, Raw: lnaRaw(t, set)}
}

// lnaStored is the state of an attachment that has been created, with attributes replaced.
func lnaStored(t *testing.T, override map[string]tftypes.Value) tfsdk.State {
	t.Helper()
	_, sch := lnaSchema(t)
	set := map[string]tftypes.Value{
		"vm_id":               nirNum(vmPanelVMID),
		"network_id":          nirNum(lnaNetworkID),
		"ip":                  nirStr(lnaAddress),
		"interface_id":        nirNum(lnaInterfaceID),
		"mac_address":         nirStr(lnaMAC),
		"region":              nirStr("TEST"),
		"project_tag":         nirStr("proj"),
		"reboot_after_attach": lnaFlag(""),
		"timeouts":            lnaTimeouts(t, "", ""),
	}
	for k, v := range override {
		set[k] = v
	}
	return tfsdk.State{Schema: sch, Raw: lnaRaw(t, set)}
}

func lnaNullState(t *testing.T) tfsdk.State {
	t.Helper()
	obj, sch := lnaSchema(t)
	return tfsdk.State{Schema: sch, Raw: tftypes.NewValue(obj, nil)}
}

func lnaNull(t tftypes.Type) tftypes.Value { return tftypes.NewValue(t, nil) }

func lnaResource(c *client.Client) *LocalNetworkAttachmentResource {
	return &LocalNetworkAttachmentResource{client: c, restartPoll: time.Millisecond}
}

func lnaCreate(t *testing.T, c *client.Client, plan tfsdk.Plan) *resource.CreateResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), lnaCeiling)
	defer cancel()
	resp := &resource.CreateResponse{State: lnaNullState(t)}
	lnaResource(c).Create(ctx, resource.CreateRequest{Plan: plan}, resp)
	return resp
}

func lnaRead(t *testing.T, c *client.Client, state tfsdk.State) *resource.ReadResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), lnaCeiling)
	defer cancel()
	resp := &resource.ReadResponse{State: state}
	lnaResource(c).Read(ctx, resource.ReadRequest{State: state}, resp)
	return resp
}

func lnaDelete(t *testing.T, c *client.Client, state tfsdk.State) *resource.DeleteResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), lnaCeiling)
	defer cancel()
	resp := &resource.DeleteResponse{State: state}
	lnaResource(c).Delete(ctx, resource.DeleteRequest{State: state}, resp)
	return resp
}

func lnaImport(t *testing.T, c *client.Client, id string) *resource.ImportStateResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), lnaCeiling)
	defer cancel()
	resp := &resource.ImportStateResponse{State: lnaNullState(t)}
	lnaResource(c).ImportState(ctx, resource.ImportStateRequest{ID: id}, resp)
	return resp
}

// lnaModel reads the attachment out of a state the resource wrote.
func lnaModel(t *testing.T, state tfsdk.State) LocalNetworkAttachmentResourceModel {
	t.Helper()
	if state.Raw.IsNull() {
		t.Fatal("the resource wrote no state")
	}
	var m LocalNetworkAttachmentResourceModel
	if d := state.Get(context.Background(), &m); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	return m
}

// lnaOnly fails the test unless the diagnostics hold exactly the errors and warnings given, by
// summary, and returns them.
func lnaOnly(t *testing.T, diags diag.Diagnostics, wantErrs, wantWarns []string) {
	t.Helper()
	errs, warns := diagSummaries(diags)
	if !slices.Equal(errs, wantErrs) || !slices.Equal(warns, wantWarns) {
		t.Errorf("errors = %q, warnings = %q; want errors %q, warnings %q\n%v", errs, warns, wantErrs, wantWarns, diags)
	}
}

// lnaDetail is the detail of the one error the diagnostics hold.
func lnaDetail(t *testing.T, diags diag.Diagnostics) string {
	t.Helper()
	for _, d := range diags {
		if d.Severity() == diag.SeverityError {
			return d.Summary() + "\n" + d.Detail()
		}
	}
	t.Fatalf("no error in %v", diags)
	return ""
}

func wantInText(t *testing.T, text string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(text, w) {
			t.Errorf("text does not contain %q:\n%s", w, text)
		}
	}
}

// wantInOrder fails the test unless every part is in the text, each after the one before it.
func wantInOrder(t *testing.T, text string, parts ...string) {
	t.Helper()
	from := 0
	for _, part := range parts {
		i := strings.Index(text[from:], part)
		if i < 0 {
			t.Errorf("%q is not in the text, or not after what the text must say before it:\n%s", part, text)
			return
		}
		from += i + len(part)
	}
}

func wantNotInText(t *testing.T, text string, nots ...string) {
	t.Helper()
	for _, n := range nots {
		if strings.Contains(text, n) {
			t.Errorf("text contains %q:\n%s", n, text)
		}
	}
}

// ---- Create ----

func TestLocalNetworkAttachmentCreate_AttachesAndRecordsTheInterface(t *testing.T) {
	c, p := newNICPanel(t, "STOPPED")

	resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

	lnaOnly(t, resp.Diagnostics, nil, nil)
	wantCalls(t, p.nicCalls(), []string{"attach"})
	if got, want := p.attachBodies(), []string{`{"networkId":412,"ip":"192.168.20.15"}`}; !slices.Equal(got, want) {
		t.Errorf("attach bodies = %v, want %v", got, want)
	}
	m := lnaModel(t, resp.State)
	if m.InterfaceID.ValueInt64() != lnaInterfaceID || m.MACAddress.ValueString() != lnaMAC || m.IP.ValueString() != lnaAddress {
		t.Errorf("interface_id/mac_address/ip = %v/%v/%v, want %d/%s/%s", m.InterfaceID, m.MACAddress, m.IP, lnaInterfaceID, lnaMAC, lnaAddress)
	}
	if m.VmID.ValueInt64() != vmPanelVMID || m.NetworkID.ValueInt64() != lnaNetworkID {
		t.Errorf("vm_id/network_id = %v/%v, want %d/%d", m.VmID, m.NetworkID, vmPanelVMID, lnaNetworkID)
	}
	if m.Region.ValueString() != "TEST" || m.ProjectTag.ValueString() != "proj" {
		t.Errorf("region/project_tag = %v/%v, want the client's defaults, TEST/proj", m.Region, m.ProjectTag)
	}
}

func TestLocalNetworkAttachmentCreate_WithoutAnAddressTheOneTheNetworkGivesIsRecorded(t *testing.T) {
	c, p := newNICPanel(t, "STOPPED")

	resp := lnaCreate(t, c, lnaCreatePlan(t, map[string]tftypes.Value{"ip": nirStrUnknown()}))

	lnaOnly(t, resp.Diagnostics, nil, nil)
	if got, want := p.attachBodies(), []string{`{"networkId":412}`}; !slices.Equal(got, want) {
		t.Errorf("attach bodies = %v, want %v: no address must be sent when none is configured", got, want)
	}
	if m := lnaModel(t, resp.State); m.IP.ValueString() != "192.168.20.2" {
		t.Errorf("ip = %v, want the address the panel handed out, 192.168.20.2", m.IP)
	}
}

func TestLocalNetworkAttachmentCreate_AnInterfaceWithoutAMACIsRecordedWithoutOne(t *testing.T) {
	c, p := newNICPanel(t, "STOPPED")
	p.noMAC = true

	resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

	lnaOnly(t, resp.Diagnostics, nil, nil)
	if m := lnaModel(t, resp.State); !m.MACAddress.IsNull() {
		t.Errorf("mac_address = %v, want null: the panel did not report one", m.MACAddress)
	}
}

func TestLocalNetworkAttachmentCreate_RegionAndProjectTravelWithTheCall(t *testing.T) {
	c, p := newNICPanel(t, "STOPPED")

	resp := lnaCreate(t, c, lnaCreatePlan(t, map[string]tftypes.Value{"region": nirStr("OTHER"), "project_tag": nirStr("other-proj")}))

	lnaOnly(t, resp.Diagnostics, nil, nil)
	if got, want := p.nicQueries(), []string{"projectTag=other-proj&region=OTHER"}; !slices.Equal(got, want) {
		t.Errorf("queries = %v, want %v", got, want)
	}
	m := lnaModel(t, resp.State)
	if m.Region.ValueString() != "OTHER" || m.ProjectTag.ValueString() != "other-proj" {
		t.Errorf("region/project_tag = %v/%v, want the ones configured", m.Region, m.ProjectTag)
	}
}

func TestLocalNetworkAttachmentCreate_RestartFlag(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   string
		flag     string
		wantPost []string
		wantWarn []string
	}{
		// A running VM: only the flag set to true restarts it, and only unset warns.
		{"running, true", "RUNNING", "true", []string{"stop", "start"}, nil},
		{"running, unset", "RUNNING", "", nil, []string{lnaSummaryRestartNeeds}},
		{"running, false", "RUNNING", "false", nil, nil},
		// A VM that is stopped applies the interface when it next starts: nothing to restart, nothing to say.
		{"stopped, true", "STOPPED", "true", nil, nil},
		{"stopped, unset", "STOPPED", "", nil, nil},
		{"stopped, false", "STOPPED", "false", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := newNICPanel(t, tc.status)

			resp := lnaCreate(t, c, lnaCreatePlan(t, map[string]tftypes.Value{"reboot_after_attach": lnaFlag(tc.flag)}))

			lnaOnly(t, resp.Diagnostics, nil, tc.wantWarn)
			wantCalls(t, p.vp.posts(), tc.wantPost)
			if tc.wantPost != nil {
				if got := p.vp.current(); got != "RUNNING" {
					t.Errorf("the VM is %s when Create returns, want RUNNING: the attachment is not created until it is back", got)
				}
			} else if got := p.vp.current(); got != tc.status {
				t.Errorf("the VM went from %s to %s, but it was not to be restarted", tc.status, got)
			}
			lnaModel(t, resp.State)
		})
	}
}

func TestLocalNetworkAttachmentCreate_TheWarningNamesTheWayOut(t *testing.T) {
	c, _ := newNICPanel(t, "RUNNING")

	resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

	wantInText(t, diagDetail(t, resp.Diagnostics, lnaSummaryRestartNeeds),
		"reboot_after_attach = true", "reboot_after_attach = false", "not a clean shutdown")
}

func TestLocalNetworkAttachmentCreate_AVMWithoutCloudInitIsToldToConfigureTheAddress(t *testing.T) {
	// Whatever the flag says about the restart, nothing sets the address up inside the guest.
	for _, flag := range []string{"", "false", "true"} {
		t.Run("flag "+flag, func(t *testing.T) {
			c, p := newNICPanel(t, "STOPPED")
			p.noCloudInit = true

			resp := lnaCreate(t, c, lnaCreatePlan(t, map[string]tftypes.Value{"reboot_after_attach": lnaFlag(flag)}))

			lnaOnly(t, resp.Diagnostics, nil, []string{lnaSummaryGuest})
			wantInText(t, diagDetail(t, resp.Diagnostics, lnaSummaryGuest), lnaAddress, lnaMAC, "VM 7")
		})
	}
}

func TestLocalNetworkAttachmentCreate_ACloudInitVMIsNotToldToConfigureAnything(t *testing.T) {
	c, _ := newNICPanel(t, "STOPPED")

	resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

	lnaOnly(t, resp.Diagnostics, nil, nil)
}

func TestLocalNetworkAttachmentCreate_FailedRestartKeepsTheInterface(t *testing.T) {
	c, p := newNICPanel(t, "RUNNING")
	p.vp.breakCall("stop", http.StatusForbidden, 714)

	resp := lnaCreate(t, c, lnaCreatePlan(t, map[string]tftypes.Value{"reboot_after_attach": lnaFlag("true")}))

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryRestartFails}, nil)
	// The interface exists on the platform: Terraform must know of it, or a retry would be refused.
	m := lnaModel(t, resp.State)
	if m.InterfaceID.ValueInt64() != lnaInterfaceID {
		t.Errorf("interface_id = %v, want %d: the interface is attached", m.InterfaceID, lnaInterfaceID)
	}
	wantInText(t, diagDetail(t, resp.Diagnostics, lnaSummaryRestartFails), "tainted", "Interface 9071", "VM 7")
}

func TestLocalNetworkAttachmentCreate_AVMThatWentDownBeforeTheRestartIsLeftAloneWithAWarning(t *testing.T) {
	c, p := newNICPanel(t, "RUNNING")
	p.stopsAfterAttach = true

	resp := lnaCreate(t, c, lnaCreatePlan(t, map[string]tftypes.Value{"reboot_after_attach": lnaFlag("true")}))

	lnaOnly(t, resp.Diagnostics, nil, []string{lnaSummaryNotRestarted})
	wantCalls(t, p.vp.posts(), nil)
	if got := p.vp.current(); got != "STOPPED" {
		t.Errorf("the VM is %s, want it left STOPPED: starting a VM that is off is not the provider's call", got)
	}
}

func TestLocalNetworkAttachmentCreate_RefusalsCarryTheirCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    answer
	}{
		{"the address is taken", answer{http.StatusConflict, 956}},
		{"the interface limit is reached", answer{http.StatusConflict, 957}},
		{"the address is not usable", answer{http.StatusBadRequest, 917}},
		{"the VM is of a kind the platform manages", answer{http.StatusForbidden, 960}},
		{"the VM is a member of a group", answer{http.StatusForbidden, 787}},
		{"a failure with a code of its own", answer{http.StatusInternalServerError, 700}},
		{"no such network", answer{http.StatusNotFound, 703}},
		{"no such network, as the other stands say it", answer{http.StatusBadRequest, 703}},
		{"no such VM", answer{http.StatusNotFound, 633}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := newNICPanel(t, "RUNNING")
			p.failEvery("attach", tc.a)

			resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

			lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
			wantInText(t, lnaDetail(t, resp.Diagnostics), "["+strconv.Itoa(tc.a.code)+"]")
			// The advice is for the catch-all alone: told to contact support over a taken address,
			// the user would not read the answer.
			wantNotInText(t, lnaDetail(t, resp.Diagnostics), "second one", "contact support")
			wantCalls(t, p.nicCalls(), []string{"attach"})
			if !resp.State.Raw.IsNull() {
				t.Error("a refused attach left a state behind")
			}
		})
	}
}

func TestLocalNetworkAttachmentCreate_ThePlatformCatchAllAdvisesAgainstRetrying(t *testing.T) {
	c, p := newNICPanel(t, "RUNNING")
	p.failEvery("attach", panelRefusal)

	resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
	wantInText(t, lnaDetail(t, resp.Diagnostics), "[627]", "second one", "contact support")
	// 627 is not a busy answer: retrying it would stall the apply and could create the interface twice.
	wantCalls(t, p.nicCalls(), []string{"attach"})
}

func TestLocalNetworkAttachmentCreate_ABusyVMIsAskedAgain(t *testing.T) {
	c, p := newNICPanel(t, "STOPPED")
	p.failNext("attach", answer{http.StatusServiceUnavailable, 961})

	resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

	lnaOnly(t, resp.Diagnostics, nil, nil)
	wantCalls(t, p.nicCalls(), []string{"attach", "attach"})
	if got := len(p.listed()); got != 1 {
		t.Errorf("the VM has %d interfaces, want the one that was attached", got)
	}
}

func TestLocalNetworkAttachmentCreate_AVMThatIsStartingIsWaitedFor(t *testing.T) {
	c, p := newNICPanel(t, "STARTING")
	p.failNext("attach", answer{http.StatusConflict, 959})
	p.vp.setScript("STARTING", "STARTING", "STARTING", "RUNNING")

	resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

	lnaOnly(t, resp.Diagnostics, nil, []string{lnaSummaryRestartNeeds})
	wantCalls(t, p.nicCalls(), []string{"attach", "attach"})
	wantCalls(t, p.vp.posts(), nil)
	lnaModel(t, resp.State)
}

func TestLocalNetworkAttachmentCreate_AVMThatWillNotSettleEndsTheWaitWithBothReasons(t *testing.T) {
	// ERROR is a status a VM does not leave by itself. The panel answers 601, not DELETED, for a VM
	// that is gone, but the wait ends all the same should it ever say so.
	for _, status := range []string{"ERROR", "DELETED"} {
		t.Run(status, func(t *testing.T) {
			c, p := newNICPanel(t, status)
			p.failEvery("attach", answer{http.StatusConflict, 959})

			resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

			lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
			detail := lnaDetail(t, resp.Diagnostics)
			wantInText(t, detail, "[959]", "status "+status, "does not leave by itself")
			// Ended by the status: a wait that went on until the create timeout would say so.
			wantNotInText(t, detail, "timed out", "interrupted")
			wantCalls(t, p.nicCalls(), []string{"attach"})
			wantCalls(t, p.vp.posts(), nil)
		})
	}
}

func TestLocalNetworkAttachmentCreate_ACreateTimeoutBoundsTheWaitForAVM(t *testing.T) {
	c, p := newNICPanel(t, "STARTING")
	p.failEvery("attach", answer{http.StatusConflict, 959})
	p.vp.setScript("STARTING", slices.Repeat([]string{"STARTING"}, 100000)...)

	start := time.Now()
	resp := lnaCreate(t, c, lnaCreatePlan(t, map[string]tftypes.Value{"timeouts": lnaTimeouts(t, "150ms", "")}))

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
	wantInText(t, lnaDetail(t, resp.Diagnostics), "timed out", "STARTING")
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Create took %v with a create timeout of 150ms", elapsed)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("a create that timed out waiting left a state behind")
	}
}

func TestLocalNetworkAttachmentCreate_AVMStatusThatCannotBeReadEndsTheWait(t *testing.T) {
	c, p := newNICPanel(t, "STARTING")
	p.failEvery("attach", answer{http.StatusConflict, 959})
	p.vp.failReadsLike(slices.Repeat([]bool{true}, 50)...)

	resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
	wantInText(t, lnaDetail(t, resp.Diagnostics), "[959]", "reading the status of VM 7", "3 consecutive failures")
}

func TestLocalNetworkAttachmentCreate_ATimeoutDuringTheWaitIsATimeoutNotAThirdFailedRead(t *testing.T) {
	// Two status reads have failed when the create timeout cuts the third one short. The wait must
	// say that it ran out of time; counting the cancelled read as a third failure in a row would
	// blame the panel for something the clock did.
	c, p := newNICPanel(t, "STARTING")
	p.failEvery("attach", answer{http.StatusConflict, 959})
	p.vp.failReadsLike(slices.Repeat([]bool{true}, vmRestartMaxPollErrs-1)...)
	p.holdStatusFrom = vmRestartMaxPollErrs
	r := &LocalNetworkAttachmentResource{client: c, restartPoll: 10 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), lnaCeiling)
	defer cancel()
	resp := &resource.CreateResponse{State: lnaNullState(t)}

	r.Create(ctx, resource.CreateRequest{Plan: lnaCreatePlan(t, map[string]tftypes.Value{"timeouts": lnaTimeouts(t, "300ms", "")})}, resp)

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
	detail := lnaDetail(t, resp.Diagnostics)
	wantInText(t, detail, "[959]", "timed out waiting for VM 7")
	wantNotInText(t, detail, "consecutive failures")
}

func TestLocalNetworkAttachmentCreate_AFewFailedStatusReadsAreForgiven(t *testing.T) {
	c, p := newNICPanel(t, "STARTING")
	p.failNext("attach", answer{http.StatusConflict, 959})
	p.vp.setScript("STARTING", "RUNNING")
	p.vp.failReadsLike(true, true, false)

	resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

	lnaOnly(t, resp.Diagnostics, nil, []string{lnaSummaryRestartNeeds})
	wantCalls(t, p.nicCalls(), []string{"attach", "attach"})
}

func TestLocalNetworkAttachmentCreate_FailedStatusReadsSeparatedBySuccessesDoNotAddUp(t *testing.T) {
	// Three failures in a row end the wait; three failures in all, with answers in between, do not.
	c, p := newNICPanel(t, "STARTING")
	p.failNext("attach", answer{http.StatusConflict, 959})
	p.vp.setScript("STARTING", "STARTING", "STARTING", "STARTING", "STARTING", "RUNNING")
	p.vp.failReadsLike(true, false, true, false, true, false, true, false)

	resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

	lnaOnly(t, resp.Diagnostics, nil, []string{lnaSummaryRestartNeeds})
	wantCalls(t, p.nicCalls(), []string{"attach", "attach"})
}

func TestLocalNetworkAttachmentCreate_AVMThatReadsSettledButIsRefusedIsAskedAgainOnlyAfterAPause(t *testing.T) {
	// The panel says 959 while its own status says STOPPED: it shows a paused VM so. Asking again at
	// once would hammer it, so the pause is the poll interval; after a few refusals in a row the
	// asking ends, with the reason.
	const pause = 40 * time.Millisecond
	c, p := newNICPanel(t, "STOPPED")
	p.failEvery("attach", answer{http.StatusConflict, 959})
	r := &LocalNetworkAttachmentResource{client: c, restartPoll: pause}
	ctx, cancel := context.WithTimeout(context.Background(), lnaCeiling)
	defer cancel()
	resp := &resource.CreateResponse{State: lnaNullState(t)}

	start := time.Now()
	r.Create(ctx, resource.CreateRequest{Plan: lnaCreatePlan(t, nil)}, resp)

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
	wantInText(t, lnaDetail(t, resp.Diagnostics), "[959]", "reports VM 7 as STOPPED", "refused the request 3 times in a row", "paused or a hibernated")
	wantCalls(t, p.nicCalls(), []string{"attach", "attach", "attach"})
	wantCalls(t, p.vp.posts(), nil)
	// Two pauses lie between three calls, and a timer never fires early.
	if elapsed := time.Since(start); elapsed < 2*pause {
		t.Errorf("Create took %v, want at least the two pauses of %v between the three calls", elapsed, pause)
	}
}

func TestLocalNetworkAttachmentCreate_ARefusalAfterARealWaitStartsTheCountOfSettledRefusalsAgain(t *testing.T) {
	// Only refusals that follow a look at a VM that was settled at once add up: one that followed a
	// wait for the VM was a race with another operation, and what comes after it starts afresh. Two
	// such refusals, a wait, two more: four in all, never three in a row.
	c, p := newNICPanel(t, "RUNNING")
	p.failNext("attach", slices.Repeat([]answer{{http.StatusConflict, 959}}, 4)...)
	p.vp.setScript("RUNNING", "RUNNING", "STARTING", "STARTING", "RUNNING")

	resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

	lnaOnly(t, resp.Diagnostics, nil, []string{lnaSummaryRestartNeeds})
	wantCalls(t, p.nicCalls(), slices.Repeat([]string{"attach"}, 5))
}

func TestLocalNetworkAttachmentCreate_AnEmptyStatusIsAVMThatIsNotSettledYet(t *testing.T) {
	// A status read that comes back without a status shows a VM that is neither RUNNING nor STOPPED
	// yet, so the refusal it follows was a race with another operation, not a paused VM: three of
	// them in a row do not end the asking.
	c, p := newNICPanel(t, "RUNNING")
	p.failNext("attach", slices.Repeat([]answer{{http.StatusConflict, 959}}, 3)...)
	p.vp.setScript("RUNNING", slices.Repeat([]string{"", "RUNNING"}, 3)...)

	resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

	lnaOnly(t, resp.Diagnostics, nil, []string{lnaSummaryRestartNeeds})
	wantCalls(t, p.nicCalls(), slices.Repeat([]string{"attach"}, 4))
}

func TestLocalNetworkAttachmentCreate_ARefusalAfterAWaitOfOnePollIsNotOneThatFoundTheVMSettled(t *testing.T) {
	// Each refusal finds the VM in transition for exactly one read and settled at the next: a wait,
	// short as it is. Five such refusals in a row are five races with another operation, never
	// three of a paused VM.
	c, p := newNICPanel(t, "STOPPED")
	p.failNext("attach", slices.Repeat([]answer{{http.StatusConflict, 959}}, 5)...)
	p.vp.setScript("STOPPED", slices.Repeat([]string{"STOPPING", "STOPPED"}, 5)...)

	resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

	lnaOnly(t, resp.Diagnostics, nil, nil)
	wantCalls(t, p.nicCalls(), slices.Repeat([]string{"attach"}, 6))
}

func TestLocalNetworkAttachmentCreate_AFailedStatusReadIsNoWaitForTheVM(t *testing.T) {
	// A paused VM reads STOPPED and is refused every time, and the status endpoint fails now and
	// then. A read that failed shows nothing of the VM, so it is no sign that the VM was waited
	// for: the refusals still add up, and the asking ends.
	c, p := newNICPanel(t, "STOPPED")
	p.failEvery("attach", answer{http.StatusConflict, 959})
	p.vp.failReadsLike(true, false, true, false, true, false)

	resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
	wantInText(t, lnaDetail(t, resp.Diagnostics), "[959]", "reports VM 7 as STOPPED", "refused the request 3 times in a row", "paused or a hibernated")
	wantCalls(t, p.nicCalls(), []string{"attach", "attach", "attach"})
}

func TestLocalNetworkAttachmentCreate_ADeadlineInTheRetriesOfABusyVMNamesTheBusyVMNotItsStatus(t *testing.T) {
	// 959 first; the VM then reads settled and the panel answers 961 (another operation holds the VM)
	// until the deadline falls into the pause before the next repeat. The VM's status is not what
	// the user has to look at.
	c, p := newNICPanel(t, "RUNNING")
	p.failNext("attach", answer{http.StatusConflict, 959})
	p.failEvery("attach", answer{http.StatusServiceUnavailable, 961})
	r := &LocalNetworkAttachmentResource{client: c, restartPoll: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), lnaCeiling)
	defer cancel()
	resp := &resource.CreateResponse{State: lnaNullState(t)}

	r.Create(ctx, resource.CreateRequest{Plan: lnaCreatePlan(t, map[string]tftypes.Value{"timeouts": lnaTimeouts(t, "300ms", "")})}, resp)

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
	detail := lnaDetail(t, resp.Diagnostics)
	wantInText(t, detail, "[961]", "timed out while another operation held the VM")
	wantNotInText(t, detail, "neither running nor stopped", "[959]")
}

func TestLocalNetworkAttachmentWhileUnsettled_ADeadlineInTheRetriesOfABusyVMKeepsTheBusyAnswer(t *testing.T) {
	c, _ := newNICPanel(t, "RUNNING")
	r := &LocalNetworkAttachmentResource{client: c, restartPoll: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	calls := 0

	err := r.whileUnsettled(ctx, vmPanelVMID, &client.RequestOpts{}, func() error {
		calls++
		if calls == 1 {
			return &client.APIError{StatusCode: http.StatusConflict, Codes: []int{959}, Message: "refused"}
		}
		return &client.APIError{StatusCode: http.StatusServiceUnavailable, Codes: []int{961}, Message: "busy"}
	})

	if !client.IsAPIError(err, 961) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the busy answer 961 kept together with the deadline", err)
	}
	wantInText(t, err.Error(), "timed out while another operation held the VM")
	wantNotInText(t, err.Error(), "neither running nor stopped")
}

func TestLocalNetworkAttachmentWhileUnsettled_AnInterruptionInTheRetriesOfABusyVMIsNotATimeout(t *testing.T) {
	// 959 first, then 961 (another operation holds the VM) until Terraform is interrupted in the
	// pause before the next repeat, which is seconds long.
	c, _ := newNICPanel(t, "RUNNING")
	r := &LocalNetworkAttachmentResource{client: c, restartPoll: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), lnaCeiling)
	defer cancel()
	calls := 0

	err := r.whileUnsettled(ctx, vmPanelVMID, &client.RequestOpts{}, func() error {
		calls++
		if calls == 1 {
			return &client.APIError{StatusCode: http.StatusConflict, Codes: []int{959}, Message: "refused"}
		}
		if calls == 2 {
			time.AfterFunc(50*time.Millisecond, cancel)
		}
		return &client.APIError{StatusCode: http.StatusServiceUnavailable, Codes: []int{961}, Message: "busy"}
	})

	if !client.IsAPIError(err, 961) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the busy answer 961 kept together with the interruption", err)
	}
	wantInText(t, err.Error(), "interrupted while another operation held the VM")
	wantNotInText(t, err.Error(), "timed out", "neither running nor stopped")
}

func TestLocalNetworkAttachmentWhileUnsettled_AnInterruptionInThePauseIsNotATimeout(t *testing.T) {
	// The VM reads settled at once, so after the 959 the call is in the pause, which is an hour long.
	c, p := newNICPanel(t, "RUNNING")
	r := &LocalNetworkAttachmentResource{client: c, restartPoll: time.Hour}
	ctx, cancel := context.WithTimeout(context.Background(), lnaCeiling)
	defer cancel()
	go func() {
		for p.vp.requestCount() < 1 && ctx.Err() == nil {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := r.whileUnsettled(ctx, vmPanelVMID, &client.RequestOpts{}, func() error {
		return &client.APIError{StatusCode: http.StatusConflict, Codes: []int{959}, Message: "refused"}
	})

	if !client.IsAPIError(err, 959) {
		t.Fatalf("err = %v, want the refusal 959 kept", err)
	}
	wantInText(t, err.Error(), "interrupted while the VM was neither running nor stopped")
	wantNotInText(t, err.Error(), "timed out")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want the interruption in its chain", err)
	}
}

func TestLocalNetworkAttachmentWhileUnsettled_AnInterruptionWhileWaitingForTheVMIsNotATimeout(t *testing.T) {
	c, p := newNICPanel(t, "STARTING")
	p.vp.setScript("STARTING", slices.Repeat([]string{"STARTING"}, 100000)...)
	r := &LocalNetworkAttachmentResource{client: c, restartPoll: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), lnaCeiling)
	defer cancel()
	go func() {
		for p.vp.requestCount() < 5 && ctx.Err() == nil {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()

	err := r.whileUnsettled(ctx, vmPanelVMID, &client.RequestOpts{}, func() error {
		return &client.APIError{StatusCode: http.StatusConflict, Codes: []int{959}, Message: "refused"}
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the interruption", err)
	}
	wantInText(t, err.Error(), "interrupted waiting for VM 7 to become RUNNING or STOPPED")
	wantNotInText(t, err.Error(), "timed out")
}

func TestLocalNetworkAttachmentWhileUnsettled_ATimeoutInThePauseAfterARefusalKeepsTheRefusal(t *testing.T) {
	// The pause is far longer than the deadline, so the deadline falls in it.
	c, _ := newNICPanel(t, "RUNNING")
	r := &LocalNetworkAttachmentResource{client: c, restartPoll: time.Hour}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := r.whileUnsettled(ctx, vmPanelVMID, &client.RequestOpts{}, func() error {
		return &client.APIError{StatusCode: http.StatusConflict, Codes: []int{959}, Message: "refused"}
	})

	if !client.IsAPIError(err, 959) {
		t.Fatalf("err = %v, want the refusal 959 kept", err)
	}
	wantInText(t, err.Error(), "timed out while the VM was neither running nor stopped")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the deadline in its chain", err)
	}
}

func TestLocalNetworkAttachmentCreate_ATimeoutThatEndsAnAttemptInFlightKeepsTheRefusalBeforeIt(t *testing.T) {
	// The panel refused once with 959; the second attempt is on the wire when the timeout runs
	// out. What the user needs is why the first one was refused, not only that a request was cut.
	// Where the deadline falls decides the wording on a machine slow enough to move it, so what is
	// checked is what every wording says.
	c, p := newNICPanel(t, "RUNNING")
	p.failNext("attach", answer{http.StatusConflict, 959})
	p.holdAttachFrom = 2
	r := &LocalNetworkAttachmentResource{client: c, restartPoll: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), lnaCeiling)
	defer cancel()
	resp := &resource.CreateResponse{State: lnaNullState(t)}

	r.Create(ctx, resource.CreateRequest{Plan: lnaCreatePlan(t, map[string]tftypes.Value{"timeouts": lnaTimeouts(t, "300ms", "")})}, resp)

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
	detail := lnaDetail(t, resp.Diagnostics)
	wantInText(t, detail, "[959]", "timed out", "No answer to the request reached the provider",
		"No interface in network 412 is listed for VM 7 (yet)")
	wantNotInText(t, detail, "interrupted")
	// The deadline has run out by the time the interfaces are listed, so they are asked for on a
	// context of their own.
	wantCalls(t, p.nicCalls(), []string{"attach", "attach", "list"})
}

func TestLocalNetworkAttachmentCreate_AnInterruptionThatCutsAnAttemptInFlightKeepsTheRefusalBeforeIt(t *testing.T) {
	// Terraform is interrupted while the second attempt is on the wire, after a 959 for the first.
	// The cancellation comes from the test at that very moment, so no timing decides the outcome.
	c, p := newNICPanel(t, "RUNNING")
	p.failNext("attach", answer{http.StatusConflict, 959})
	p.holdAttachFrom = 2
	ctx, cancel := context.WithTimeout(context.Background(), lnaCeiling)
	defer cancel()
	p.onHold = cancel
	resp := &resource.CreateResponse{State: lnaNullState(t)}

	lnaResource(c).Create(ctx, resource.CreateRequest{Plan: lnaCreatePlan(t, nil)}, resp)

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
	detail := lnaDetail(t, resp.Diagnostics)
	wantInText(t, detail, "[959]", "interrupted while the VM was neither running nor stopped", "context canceled",
		"No answer to the request reached the provider", "listed by GET /api/v2/vms/7/local-networks")
	wantNotInText(t, detail, "timed out", "(yet)")
	// Terraform is stopping: not the moment for one more request, so the interfaces are not listed.
	wantCalls(t, p.nicCalls(), []string{"attach", "attach"})
}

func TestLocalNetworkAttachmentCreate_AnotherRefusalAfterA959IsNotMistakenForATimeout(t *testing.T) {
	// 959 first, then the VM settles and the panel refuses for a different reason: that reason is
	// the answer, and the clock has nothing to do with it.
	c, p := newNICPanel(t, "RUNNING")
	p.failNext("attach", answer{http.StatusConflict, 959}, answer{http.StatusConflict, 955})
	r := &LocalNetworkAttachmentResource{client: c, restartPoll: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), lnaCeiling)
	defer cancel()
	resp := &resource.CreateResponse{State: lnaNullState(t)}

	r.Create(ctx, resource.CreateRequest{Plan: lnaCreatePlan(t, nil)}, resp)

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
	detail := lnaDetail(t, resp.Diagnostics)
	wantInText(t, detail, "[955]")
	if strings.Contains(detail, "neither running nor stopped") {
		t.Errorf("a refusal that is not 959 was reported as a timeout:\n%s", detail)
	}
	wantCalls(t, p.nicCalls(), []string{"attach", "attach", "list"})
}

func TestLocalNetworkAttachmentWhileUnsettled_ARefusalIsNotATimeoutEvenIfTheDeadlinePassedWithIt(t *testing.T) {
	// The deadline passes while the panel's answer is on its way back, and the answer is a refusal
	// of its own: the refusal is what happened, the clock only happened to run out with it.
	c, _ := newNICPanel(t, "RUNNING")
	r := &LocalNetworkAttachmentResource{client: c, restartPoll: time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	refusal := func(code int) error {
		return &client.APIError{StatusCode: http.StatusConflict, Codes: []int{code}, Message: "refused"}
	}
	calls := 0

	err := r.whileUnsettled(ctx, vmPanelVMID, &client.RequestOpts{}, func() error {
		calls++
		if calls == 1 {
			return refusal(959)
		}
		cancel()
		return refusal(955)
	})

	if !client.IsAPIError(err, 955) {
		t.Fatalf("err = %v, want the refusal 955", err)
	}
	if strings.Contains(err.Error(), "neither running nor stopped") {
		t.Errorf("a refusal that is not 959 was reported as a timeout: %v", err)
	}
	if calls != 2 {
		t.Errorf("%d calls, want 2", calls)
	}
}

func TestLocalNetworkAttachmentCreate_AnAttemptCutByTheTimeoutWithoutARefusalBeforeItIsReportedAsItIs(t *testing.T) {
	c, p := newNICPanel(t, "RUNNING")
	p.holdAttachFrom = 1
	r := &LocalNetworkAttachmentResource{client: c, restartPoll: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), lnaCeiling)
	defer cancel()
	resp := &resource.CreateResponse{State: lnaNullState(t)}

	r.Create(ctx, resource.CreateRequest{Plan: lnaCreatePlan(t, map[string]tftypes.Value{"timeouts": lnaTimeouts(t, "300ms", "")})}, resp)

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
	detail := lnaDetail(t, resp.Diagnostics)
	wantInText(t, detail, "context deadline exceeded")
	if strings.Contains(detail, "neither running nor stopped") {
		t.Errorf("no 959 came before the cut, so the VM's status is not what timed out:\n%s", detail)
	}
}

func TestLocalNetworkAttachmentCreate_AVMThatIsStillStartingIsReadAtTheIntervalAndNotInATightLoop(t *testing.T) {
	c, p := newNICPanel(t, "STARTING")
	p.failEvery("attach", answer{http.StatusConflict, 959})
	p.vp.setScript("STARTING", slices.Repeat([]string{"STARTING"}, 100000)...)
	r := &LocalNetworkAttachmentResource{client: c, restartPoll: 25 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), lnaCeiling)
	defer cancel()
	resp := &resource.CreateResponse{State: lnaNullState(t)}

	r.Create(ctx, resource.CreateRequest{Plan: lnaCreatePlan(t, map[string]tftypes.Value{"timeouts": lnaTimeouts(t, "300ms", "")})}, resp)

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
	reads := p.vp.requestCount()
	if reads < 2 {
		t.Errorf("%d status reads, want the VM read again after the interval", reads)
	}
	// 300 ms at one read per 25 ms is 12 reads, and a few more for the slack of a loaded machine.
	if reads > 25 {
		t.Errorf("%d status reads in 300ms with a 25ms interval: the interval is not kept", reads)
	}
}

func TestLocalNetworkAttachmentCreate_ANetworkTheVMIsAlreadyInNamesTheInterfaceToImport(t *testing.T) {
	// The interface was created by an apply that was cut short before Terraform recorded it.
	c, p := newNICPanel(t, "RUNNING")
	p.has(mainNIC())
	p.has(stored())
	p.failEvery("attach", answer{http.StatusConflict, 955})

	resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
	detail := lnaDetail(t, resp.Diagnostics)
	wantInText(t, detail, "[955]", "VM 7 has interface 9071 in network 412 (address "+lnaAddress+", MAC address "+lnaMAC+")",
		"`terraform import prodata_local_network_attachment.<name> 7:9071`", "Otherwise choose another network")
	wantNotInText(t, detail, "contact support", "second one", "No answer to the request")
	wantCalls(t, p.nicCalls(), []string{"attach", "list"})
	if !resp.State.Raw.IsNull() {
		t.Error("a refused attach left a state behind")
	}
}

func TestLocalNetworkAttachmentCreate_TheVMsOwnNetworkIsNotOfferedForImport(t *testing.T) {
	// The network is the one the VM was created in (a typo, a copied block): the interface in it is
	// the primary one, which the import refuses. Advising to import it would send the user round in
	// a circle.
	c, p := newNICPanel(t, "RUNNING")
	p.has(mainNIC())
	p.failEvery("attach", answer{http.StatusConflict, 955})

	resp := lnaCreate(t, c, lnaCreatePlan(t, map[string]tftypes.Value{"network_id": nirNum(411)}))

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
	detail := lnaDetail(t, resp.Diagnostics)
	wantInText(t, detail, "[955]", "Network 411 is the VM's own network", "interface 9070 is its primary interface", "Choose another network.")
	wantNotInText(t, detail, "terraform import", "Otherwise choose another network", "If an earlier apply", "(yet)")
	wantCalls(t, p.nicCalls(), []string{"attach", "list"})
}

func TestLocalNetworkAttachmentCreate_APrimaryInterfaceThatCarriesAGatewayIsNotCalledTheVMsOwnNetwork(t *testing.T) {
	// The platform treats an interface with a gateway as primary wherever it is, and the list does not
	// say whether it is the VM's main one. The user is not told to remove its gateway as a matter of
	// course, nor sent to another network without being told the other way.
	for _, tc := range []struct {
		name    string
		gateway *string
		want    []string
		wantNot []string
	}{
		{"none recorded", nil,
			[]string{"Network 412 is the VM's own network", "interface 9071 is its primary interface", "Choose another network."},
			[]string{"default gateway", "move the gateway", "No gateway"}},
		{"an empty one recorded", ptr(""),
			[]string{"Network 412 is the VM's own network", "Choose another network."},
			[]string{"default gateway", "move the gateway", "No gateway"}},
		{"one recorded", ptr("192.168.20.1"),
			[]string{"VM 7 has interface 9071 in network 412, and the platform treats it as primary",
				"network 412 is the VM's own network, or the interface carries a default gateway",
				"This resource does not manage a primary interface and the platform does not detach it. " +
					"If network 412 is the VM's own network, choose another network. " +
					"If it is not, and the interface does not need to carry the gateway, " + lnaMoveGateway +
					" Then run `terraform import prodata_local_network_attachment.<name> 7:9071` and apply again."},
			[]string{"Network 412 is the VM's own network", "If an earlier apply", "Otherwise choose another network", "(yet)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := newNICPanel(t, "RUNNING")
			nic := stored()
			nic.Primary = true
			nic.Gateway = tc.gateway
			p.has(nic)
			p.failEvery("attach", answer{http.StatusConflict, 955})

			resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

			lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
			detail := lnaDetail(t, resp.Diagnostics)
			wantInText(t, detail, append([]string{"[955]"}, tc.want...)...)
			wantNotInText(t, detail, tc.wantNot...)
			wantCalls(t, p.nicCalls(), []string{"attach", "list"})
		})
	}
}

func TestLocalNetworkAttachmentCreate_TheFlagNotTheGatewayDecidesThatAnInterfaceIsPrimary(t *testing.T) {
	// The panel sends primary = true for an interface with a gateway, so the two come together. The
	// provider goes by the flag, as the import does: an interface that is not primary is taken over,
	// whatever else it carries.
	c, p := newNICPanel(t, "RUNNING")
	nic := stored()
	nic.Gateway = ptr("192.168.20.1")
	p.has(nic)
	p.failEvery("attach", answer{http.StatusConflict, 955})

	resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
	detail := lnaDetail(t, resp.Diagnostics)
	wantInText(t, detail, "[955]", "VM 7 has interface 9071 in network 412 (address "+lnaAddress+", MAC address "+lnaMAC+")",
		"`terraform import prodata_local_network_attachment.<name> 7:9071`", "Otherwise choose another network")
	wantNotInText(t, detail, "treats it as primary", "default gateway", "move the gateway", "No gateway")
}

func TestLocalNetworkAttachmentCreate_AnAttachThatGotNoAnswerOnAPrimaryInterfaceWithAGatewaySaysSo(t *testing.T) {
	// The panel would have refused the attach all the same: the VM has an interface in the network.
	c, p := newNICPanel(t, "RUNNING")
	nic := stored()
	nic.Primary = true
	nic.Gateway = ptr("192.168.20.1")
	p.has(nic)
	p.failEvery("attach", gatewayTimeout)

	resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
	detail := lnaDetail(t, resp.Diagnostics)
	wantInText(t, detail, "the platform treats it as primary", "carries a default gateway")
	wantNotInText(t, detail, "may have attached the interface")
}

func TestLocalNetworkAttachmentCreate_AnAttachThatGotNoAnswerOnTheVMsOwnNetworkSaysSo(t *testing.T) {
	// The panel would have refused the attach: the interface cannot have been created.
	c, p := newNICPanel(t, "RUNNING")
	p.has(mainNIC())
	p.failEvery("attach", gatewayTimeout)

	resp := lnaCreate(t, c, lnaCreatePlan(t, map[string]tftypes.Value{"network_id": nirNum(411)}))

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
	detail := lnaDetail(t, resp.Diagnostics)
	wantInText(t, detail, "Network 411 is the VM's own network", "Choose another network.")
	wantNotInText(t, detail, "may have attached the interface", "terraform import")
}

func TestLocalNetworkAttachmentCreate_AnInterfaceThatCanBeTakenOverIsPreferredToThePrimaryOne(t *testing.T) {
	// Old data can hold two interfaces of a VM in one network. The one to import is the one that is
	// not primary, wherever it stands in the list.
	c, p := newNICPanel(t, "RUNNING")
	p.has(mainNIC())
	duplicate := stored()
	duplicate.ID = 9072
	duplicate.NetworkID = ptr(int64(411))
	p.has(duplicate)
	p.failEvery("attach", answer{http.StatusConflict, 955})

	resp := lnaCreate(t, c, lnaCreatePlan(t, map[string]tftypes.Value{"network_id": nirNum(411)}))

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
	detail := lnaDetail(t, resp.Diagnostics)
	wantInText(t, detail, "VM 7 has interface 9072 in network 411", "`terraform import prodata_local_network_attachment.<name> 7:9072`")
	wantNotInText(t, detail, "own network")
}

func TestLocalNetworkAttachmentCreate_OfTwoPrimaryInterfacesTheOneWithAGatewayIsNamed(t *testing.T) {
	// Old data can hold two primary interfaces of a VM in one network: the one that is primary for the
	// network the VM was created in, and one that is primary for the gateway it carries. The advice on the
	// gateway is about the second; the first has none to move. The order of the list does not decide.
	for _, tc := range []struct {
		name         string
		gatewayFirst bool
	}{
		{"the one with the gateway first", true},
		{"the one with the gateway last", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := newNICPanel(t, "RUNNING")
			plain := stored()
			plain.Primary = true
			carrier := stored()
			carrier.ID = 9072
			carrier.Primary = true
			carrier.Gateway = ptr("192.168.20.1")
			if tc.gatewayFirst {
				p.has(carrier)
				p.has(plain)
			} else {
				p.has(plain)
				p.has(carrier)
			}
			p.failEvery("attach", answer{http.StatusConflict, 955})

			resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

			lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
			detail := lnaDetail(t, resp.Diagnostics)
			wantInText(t, detail, "VM 7 has interface 9072 in network 412, and the platform treats it as primary",
				"Then run `terraform import prodata_local_network_attachment.<name> 7:9072` and apply again.")
			wantNotInText(t, detail, "9071", "Network 412 is the VM's own network")
		})
	}
}

func TestLocalNetworkAttachmentCreate_OfPrimaryInterfacesWithAGatewayTheFirstOfTheListIsNamed(t *testing.T) {
	// The interface that is named must not depend on anything but the gateway and the order of the list: of
	// several that carry a gateway it is the first, and an empty gateway is no gateway, before or after a real one.
	primary := func(id int64, gateway *string) client.VmLocalNetwork {
		nic := stored()
		nic.ID = id
		nic.Primary = true
		nic.Gateway = gateway
		return nic
	}
	gw := ptr("192.168.20.1")
	for _, tc := range []struct {
		name  string
		nics  []client.VmLocalNetwork
		named int64
		own   bool // none of them carries a gateway: the network is taken for the VM's own
	}{
		{"two with a gateway", []client.VmLocalNetwork{primary(9072, gw), primary(9073, gw)}, 9072, false},
		{"two with a gateway, the other way round", []client.VmLocalNetwork{primary(9073, gw), primary(9072, gw)}, 9073, false},
		{"one with an empty gateway, then a real one", []client.VmLocalNetwork{primary(9071, ptr("")), primary(9072, gw)}, 9072, false},
		{"one with a real gateway, then an empty one", []client.VmLocalNetwork{primary(9072, gw), primary(9071, ptr(""))}, 9072, false},
		{"none with a gateway, one of them lists an empty one", []client.VmLocalNetwork{primary(9071, nil), primary(9072, ptr(""))}, 9071, true},
		{"none with a gateway, the other way round", []client.VmLocalNetwork{primary(9072, ptr("")), primary(9071, nil)}, 9072, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := newNICPanel(t, "RUNNING")
			for _, nic := range tc.nics {
				p.has(nic)
			}
			p.failEvery("attach", answer{http.StatusConflict, 955})

			resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

			lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
			detail := lnaDetail(t, resp.Diagnostics)
			var notNamed []string
			for _, nic := range tc.nics {
				if nic.ID != tc.named {
					notNamed = append(notNamed, fmt.Sprintf("interface %d", nic.ID))
				}
			}
			if tc.own {
				wantInText(t, detail, fmt.Sprintf("Network 412 is the VM's own network: interface %d is its primary interface", tc.named))
				wantNotInText(t, detail, append(notNamed, "Then run")...)
				return
			}
			wantInText(t, detail, fmt.Sprintf("VM 7 has interface %d in network 412, and the platform treats it as primary", tc.named),
				fmt.Sprintf("Then run `terraform import prodata_local_network_attachment.<name> 7:%d` and apply again.", tc.named))
			wantNotInText(t, detail, append(notNamed, "Network 412 is the VM's own network")...)
		})
	}
}

func TestLocalNetworkAttachmentCreate_TheInterfaceToImportIsLookedForInTheProjectOfTheResource(t *testing.T) {
	// The VM lives in the project the resource names, not in the provider's. Asked in the provider's
	// project the list answers 633 and the interface would go unnamed.
	c, p := newNICPanel(t, "RUNNING")
	p.inProject("other")
	p.has(mainNIC())
	p.has(stored())
	p.failEvery("attach", answer{http.StatusConflict, 955})

	resp := lnaCreate(t, c, lnaCreatePlan(t, map[string]tftypes.Value{"project_tag": nirStr("other")}))

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
	wantInText(t, lnaDetail(t, resp.Diagnostics), "[955]", "VM 7 has interface 9071 in network 412")
}

func TestLocalNetworkAttachmentCreate_ANetworkTheVMIsAlreadyInWithTheListUnreadableStillSaysHowToImport(t *testing.T) {
	c, p := newNICPanel(t, "RUNNING")
	p.failEvery("attach", answer{http.StatusConflict, 955})
	p.failEvery("list", badGateway)

	resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
	detail := lnaDetail(t, resp.Diagnostics)
	wantInText(t, detail, "[955]", "GET /api/v2/vms/7/local-networks", "`terraform import prodata_local_network_attachment.<name> 7:<interface_id>`")
	wantNotInText(t, detail, "(yet)", "VM 7 has interface")
}

func TestLocalNetworkAttachmentCreate_AnAttachThatGotNoAnswerNamesTheInterfaceItMayHaveCreated(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    answer
	}{
		{"a gateway that gave up", gatewayTimeout},
		{"a bad gateway", badGateway},
		{"a bare internal error page", answer{http.StatusInternalServerError, 0}},
		{"a dropped connection", droppedAnswer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The panel carried the attach out, so the interface is listed; only the answer was lost.
			c, p := newNICPanel(t, "RUNNING")
			p.has(mainNIC())
			p.has(stored())
			p.failNext("attach", tc.a)

			resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

			lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
			detail := lnaDetail(t, resp.Diagnostics)
			wantInText(t, detail, "No answer to the request reached the provider",
				"VM 7 has interface 9071 in network 412 (address "+lnaAddress+", MAC address "+lnaMAC+")",
				"`terraform import prodata_local_network_attachment.<name> 7:9071`")
			wantNotInText(t, detail, "contact support", "(yet)")
			// A request that may have been carried out is not repeated, and the VM is looked at once.
			wantCalls(t, p.nicCalls(), []string{"attach", "list"})
			if !resp.State.Raw.IsNull() {
				t.Error("an attach with no answer left a state behind")
			}
		})
	}
}

func TestLocalNetworkAttachmentCreate_AnAttachThatGotNoAnswerWhenNothingIsListedSaysToLookAgain(t *testing.T) {
	c, p := newNICPanel(t, "RUNNING")
	p.failNext("attach", gatewayTimeout)

	resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
	detail := lnaDetail(t, resp.Diagnostics)
	wantInText(t, detail, "No answer to the request reached the provider", "No interface in network 412 is listed for VM 7 (yet)",
		"GET /api/v2/vms/7/local-networks", "7:<interface_id>")
	wantNotInText(t, detail, "VM 7 has interface")
}

func TestLocalNetworkAttachmentCreate_AnAttachThatGotNoAnswerWithAnUnreadableListStillSaysWhereToLook(t *testing.T) {
	c, p := newNICPanel(t, "RUNNING")
	p.failNext("attach", gatewayTimeout)
	p.failEvery("list", badGateway)

	resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
	detail := lnaDetail(t, resp.Diagnostics)
	wantInText(t, detail, "No answer to the request reached the provider", "listed by GET /api/v2/vms/7/local-networks", "7:<interface_id>")
	wantNotInText(t, detail, "(yet)", "VM 7 has interface")
}

func TestLocalNetworkAttachmentCreate_ARequestCutByTheTimeoutNamesTheInterfaceItMayHaveCreated(t *testing.T) {
	// The create timeout has run out when the interfaces are listed: the list is asked on a context
	// of its own, or the answer to the one question that matters would always be "cannot tell".
	c, p := newNICPanel(t, "RUNNING")
	p.has(mainNIC())
	p.has(stored())
	p.holdAttachFrom = 1

	resp := lnaCreate(t, c, lnaCreatePlan(t, map[string]tftypes.Value{"timeouts": lnaTimeouts(t, "300ms", "")}))

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
	detail := lnaDetail(t, resp.Diagnostics)
	wantInText(t, detail, "context deadline exceeded", "No answer to the request reached the provider",
		"VM 7 has interface 9071 in network 412", "7:9071")
	wantCalls(t, p.nicCalls(), []string{"attach", "list"})
}

func TestLocalNetworkAttachmentCreate_ALookupThatIsNeverAnsweredIsGivenUpOn(t *testing.T) {
	// The interfaces are listed on a context that neither times out with the create nor is cancelled
	// with it, or the list would never be asked once the create timeout has run out. Its own limit is
	// then all that keeps a panel that does not answer from holding the apply, Ctrl-C included.
	c, p := newNICPanel(t, "RUNNING")
	p.failNext("attach", gatewayTimeout)
	p.holdListFrom = 1
	r := &LocalNetworkAttachmentResource{client: c, restartPoll: time.Millisecond, lookupTimeout: 100 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), lnaCeiling)
	defer cancel()
	plan := lnaCreatePlan(t, nil)
	resp := &resource.CreateResponse{State: lnaNullState(t)}

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Create(ctx, resource.CreateRequest{Plan: plan}, resp)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Create was still waiting 5s after a lookup limit of 100ms")
	}

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
	wantInText(t, lnaDetail(t, resp.Diagnostics), "No answer to the request reached the provider",
		"listed by GET /api/v2/vms/7/local-networks", "7:<interface_id>")
	wantCalls(t, p.nicCalls(), []string{"attach", "list"})
}

func TestLocalNetworkAttachmentCreate_ABareClientErrorPageIsNotMistakenForALostRequest(t *testing.T) {
	// A 4xx page is an answer: the panel was reached and did not take the request up.
	c, p := newNICPanel(t, "RUNNING")
	p.failEvery("attach", answer{http.StatusNotFound, 0})

	resp := lnaCreate(t, c, lnaCreatePlan(t, nil))

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryFailed}, nil)
	wantNotInText(t, lnaDetail(t, resp.Diagnostics), "No answer to the request", "terraform import")
	wantCalls(t, p.nicCalls(), []string{"attach"})
}

func TestLocalNetworkAttachmentCreate_ACreateTimeoutBoundsTheRestartToo(t *testing.T) {
	// The VM accepts the stop and then stays STOPPING. A restart that ignored the create timeout
	// would wait for as long as the VM does, so the create is run with a limit of its own.
	c, p := newNICPanel(t, "RUNNING")
	p.vp.setSteps([]string{"STOPPING"}, nil)
	plan := lnaCreatePlan(t, map[string]tftypes.Value{
		"reboot_after_attach": lnaFlag("true"),
		"timeouts":            lnaTimeouts(t, "300ms", ""),
	})

	done := make(chan *resource.CreateResponse, 1)
	go func() { done <- lnaCreate(t, c, plan) }()
	var resp *resource.CreateResponse
	select {
	case resp = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Create was still running 5s after a create timeout of 300ms: the restart is not bounded by it")
	}

	lnaOnly(t, resp.Diagnostics, []string{lnaSummaryRestartFails}, nil)
	wantInText(t, lnaDetail(t, resp.Diagnostics), "timed out", "waiting for VM 7 to stop")
	if m := lnaModel(t, resp.State); m.InterfaceID.ValueInt64() != lnaInterfaceID {
		t.Errorf("interface_id = %v, want %d: the interface is attached and must be kept", m.InterfaceID, lnaInterfaceID)
	}
}

// ---- Read ----

func TestLocalNetworkAttachmentRead_RefreshesTheMACAndKeepsTheOptions(t *testing.T) {
	c, p := newNICPanel(t, "RUNNING")
	nic := stored()
	nic.MAC = ptr("BC:24:11:00:00:09")
	p.has(nic)
	prior := lnaStored(t, map[string]tftypes.Value{
		"reboot_after_attach": lnaFlag("true"),
		"timeouts":            lnaTimeouts(t, "30m", "20m"),
	})

	resp := lnaRead(t, c, prior)

	lnaOnly(t, resp.Diagnostics, nil, nil)
	wantCalls(t, p.nicCalls(), []string{"list"})
	wantCalls(t, p.vp.posts(), nil)
	m := lnaModel(t, resp.State)
	if m.MACAddress.ValueString() != "BC:24:11:00:00:09" {
		t.Errorf("mac_address = %v, want the one the panel reports", m.MACAddress)
	}
	if m.InterfaceID.ValueInt64() != lnaInterfaceID || m.NetworkID.ValueInt64() != lnaNetworkID || m.IP.ValueString() != lnaAddress {
		t.Errorf("Read changed the identity of the attachment: %+v", m)
	}
	if !m.RebootAfterAttach.ValueBool() {
		t.Errorf("reboot_after_attach = %v, want true: it is not read from the panel", m.RebootAfterAttach)
	}
	if got, diags := m.Timeouts.Delete(context.Background(), 0); diags.HasError() || got != 20*time.Minute {
		t.Errorf("stored delete timeout = %v (%v), want 20m", got, diags)
	}
}

func TestLocalNetworkAttachmentRead_AMACTheCreateDidNotKnowIsPickedUp(t *testing.T) {
	c, p := newNICPanel(t, "RUNNING")
	p.has(stored())
	prior := lnaStored(t, map[string]tftypes.Value{"mac_address": lnaNull(tftypes.String)})

	resp := lnaRead(t, c, prior)

	lnaOnly(t, resp.Diagnostics, nil, nil)
	if m := lnaModel(t, resp.State); m.MACAddress.ValueString() != lnaMAC {
		t.Errorf("mac_address = %v, want %s", m.MACAddress, lnaMAC)
	}
}

func TestLocalNetworkAttachmentRead_WhatIsGoneIsRemoved(t *testing.T) {
	otherNetwork := stored()
	otherNetwork.NetworkID = ptr(int64(lnaNetworkID + 1))
	noNetwork := stored()
	noNetwork.NetworkID, noNetwork.NetworkName = nil, nil
	anotherInterface := stored()
	anotherInterface.ID = lnaInterfaceID + 1

	for _, tc := range []struct {
		name string
		set  func(p *nicPanel)
	}{
		{"the VM is not found, as the test stand says", func(p *nicPanel) { p.failEvery("list", answer{http.StatusNotFound, 633}) }},
		{"the VM is not found, answered as a bad request", func(p *nicPanel) { p.failEvery("list", answer{http.StatusBadRequest, 633}) }},
		{"the interface is not in the list", func(p *nicPanel) { p.has(anotherInterface) }},
		{"the VM has no interfaces", func(p *nicPanel) {}},
		{"the interface is in another network now", func(p *nicPanel) { p.has(otherNetwork) }},
		{"the interface is tied to no network now", func(p *nicPanel) { p.has(noNetwork) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := newNICPanel(t, "RUNNING")
			tc.set(p)

			resp := lnaRead(t, c, lnaStored(t, nil))

			lnaOnly(t, resp.Diagnostics, nil, nil)
			if !resp.State.Raw.IsNull() {
				t.Errorf("the resource is still in the state: %v", resp.State.Raw)
			}
		})
	}
}

func TestLocalNetworkAttachmentRead_AnErrorThatIsNotAnAbsenceKeepsTheResource(t *testing.T) {
	// Dropping the resource on an error that says nothing about the interface would have the next
	// apply attach it a second time.
	for _, tc := range []struct {
		name string
		a    answer
	}{
		{"the platform's catch-all", panelRefusal},
		{"a blocked user", answer{http.StatusForbidden, 601}},
		{"a bad gateway", badGateway},
		{"forbidden", answer{http.StatusForbidden, 714}},
		{"a dropped connection", droppedAnswer},
		// What a panel that does not have the routes yet answers: nothing is known about the VM.
		{"a not-found page without the panel's envelope", answer{http.StatusNotFound, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := newNICPanel(t, "RUNNING")
			p.has(stored())
			p.failEvery("list", tc.a)

			resp := lnaRead(t, c, lnaStored(t, nil))

			lnaOnly(t, resp.Diagnostics, []string{"Unable to Read Local Network Attachment"}, nil)
			if resp.State.Raw.IsNull() {
				t.Error("the resource was removed from the state because of an error that does not say the interface is gone")
			}
		})
	}
}

func TestLocalNetworkAttachmentRead_AfterAnImportTheNetworkAndTheAddressAreFilledIn(t *testing.T) {
	c, p := newNICPanel(t, "RUNNING")
	p.has(stored())
	imported := lnaStored(t, map[string]tftypes.Value{
		"network_id":  lnaNull(tftypes.Number),
		"ip":          lnaNull(tftypes.String),
		"mac_address": lnaNull(tftypes.String),
	})

	resp := lnaRead(t, c, imported)

	lnaOnly(t, resp.Diagnostics, nil, nil)
	m := lnaModel(t, resp.State)
	if m.NetworkID.ValueInt64() != lnaNetworkID || m.IP.ValueString() != lnaAddress || m.MACAddress.ValueString() != lnaMAC {
		t.Errorf("network_id/ip/mac_address = %v/%v/%v, want %d/%s/%s", m.NetworkID, m.IP, m.MACAddress, lnaNetworkID, lnaAddress, lnaMAC)
	}
	if !m.RebootAfterAttach.IsNull() || !m.Timeouts.IsNull() {
		t.Errorf("reboot_after_attach/timeouts = %v/%v, want both null: they are not read from the panel", m.RebootAfterAttach, m.Timeouts)
	}
}

func TestLocalNetworkAttachmentRead_AnImportedInterfaceWithoutANetworkIsRefused(t *testing.T) {
	c, p := newNICPanel(t, "RUNNING")
	legacy := stored()
	legacy.NetworkID, legacy.NetworkName = nil, nil
	p.has(legacy)
	imported := lnaStored(t, map[string]tftypes.Value{"network_id": lnaNull(tftypes.Number), "ip": lnaNull(tftypes.String)})

	resp := lnaRead(t, c, imported)

	lnaOnly(t, resp.Diagnostics, []string{"Unable to Read Local Network Attachment"}, nil)
	wantInText(t, lnaDetail(t, resp.Diagnostics), "not tied to a local network")
}

func TestLocalNetworkAttachmentRead_ARecordedAddressIsNeverOverwritten(t *testing.T) {
	// The configuration is not visible to Read. An address written into the state over a different
	// one in the configuration would show up as a change of ip, and ip requires replacement. Setting
	// ip alone is no way out either, so the warning says what is: take the interface over again, or
	// replace it.
	const (
		keep     = "terraform state rm"
		reimport = "terraform import <address of this resource> 7:9071"
		replace  = "terraform apply -replace=<address of this resource>"
	)
	for _, tc := range []struct {
		name     string
		panel    *string
		wantWarn []string
		wantText []string
		notText  []string
	}{
		{"the same address", ptr(lnaAddress), nil, nil, nil},
		{"another address", ptr("192.168.20.99"), []string{lnaSummaryDrift}, []string{"192.168.20.99", keep, reimport, replace, "forces a new resource"}, nil},
		{"no address", nil, []string{lnaSummaryDrift}, []string{"(unknown)", replace}, []string{keep, reimport}},
		{"an empty address", ptr(""), []string{lnaSummaryDrift}, []string{"(unknown)", replace}, []string{keep, reimport}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := newNICPanel(t, "RUNNING")
			nic := stored()
			nic.IP = tc.panel
			p.has(nic)

			resp := lnaRead(t, c, lnaStored(t, nil))

			lnaOnly(t, resp.Diagnostics, nil, tc.wantWarn)
			if m := lnaModel(t, resp.State); m.IP.ValueString() != lnaAddress {
				t.Errorf("ip = %v, want %s: Read must keep the address of the state", m.IP, lnaAddress)
			}
			if tc.wantText != nil {
				detail := diagDetail(t, resp.Diagnostics, lnaSummaryDrift)
				wantInText(t, detail, append(tc.wantText, lnaAddress, "9071", "VM 7")...)
				wantNotInText(t, detail, tc.notText...)
			}
		})
	}
}

func TestLocalNetworkAttachmentRead_ADriftedAddressOfAPrimaryInterfaceSaysThatNoWayOutWorksYet(t *testing.T) {
	// Editing the address of an interface in the console gives it the gateway of the VM, and that makes it
	// primary: the platform does not detach such an interface and an import refuses it. Every way out of
	// the drift leads through one or the other, so the warning says what has to come first.
	const (
		lead = "The state is left as it is. The interface is a primary one now, for example because a default gateway was set on it, " +
			"or its address was edited, in the console. The platform does not detach a primary interface and this resource does not import one, so "
		keep    = " To keep the address of the panel, set ip to it in the configuration, then remove the attachment from the state"
		replace = " To get the configured address back, replace the attachment: terraform apply -replace=<address of this resource>."
	)
	for _, tc := range []struct {
		name    string
		panel   *string
		primary bool
		gateway *string
		want    []string
		wantNot []string
	}{
		{"another address", ptr("192.168.20.99"), true, ptr("192.168.20.1"),
			[]string{lead + "neither of the ways below works until you " + lnaMoveGateway + keep, replace}, nil},
		{"no address", nil, true, ptr("192.168.20.1"),
			[]string{lead + "the way below does not work until you " + lnaMoveGateway + replace}, []string{"state rm", "neither of the ways"}},
		{"an empty address", ptr(""), true, ptr("192.168.20.1"),
			[]string{lead + "the way below does not work until you " + lnaMoveGateway + replace}, []string{"state rm", "neither of the ways"}},
		{"another address, interface not primary", ptr("192.168.20.99"), false, nil,
			[]string{"The state is left as it is." + keep, replace}, []string{"primary", "move the gateway", "No gateway"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := newNICPanel(t, "RUNNING")
			nic := stored()
			nic.IP = tc.panel
			nic.Primary = tc.primary
			nic.Gateway = tc.gateway
			p.has(nic)

			resp := lnaRead(t, c, lnaStored(t, nil))

			lnaOnly(t, resp.Diagnostics, nil, []string{lnaSummaryDrift})
			if m := lnaModel(t, resp.State); m.IP.ValueString() != lnaAddress {
				t.Errorf("ip = %v, want %s: Read must keep the address of the state", m.IP, lnaAddress)
			}
			detail := diagDetail(t, resp.Diagnostics, lnaSummaryDrift)
			wantInText(t, detail, tc.want...)
			wantNotInText(t, detail, tc.wantNot...)
		})
	}
}

func TestLocalNetworkAttachmentRead_ADriftedAddressOfAPrimaryInterfaceWithoutAGatewayIsForSupport(t *testing.T) {
	// An interface that is primary without a gateway is the VM's own, or shares the slot of a primary one:
	// moving a gateway changes nothing for it. ImportState gives the advice about a gateway only to an
	// interface that lists one, and the drift warning must not send the user round that circle either.
	const want = "The state is left as it is. The interface is a primary one now, although the panel lists no gateway on it, " +
		"so moving a gateway would not change that. The platform does not detach a primary interface and this resource does not import one, " +
		"so neither taking the interface over again nor replacing the attachment works: ask support why the platform treats it as primary."
	for _, tc := range []struct {
		name    string
		panel   *string
		gateway *string
	}{
		{"another address, no gateway", ptr("192.168.20.99"), nil},
		{"another address, an empty gateway", ptr("192.168.20.99"), ptr("")},
		{"no address, no gateway", nil, nil},
		{"an empty address, an empty gateway", ptr(""), ptr("")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := newNICPanel(t, "RUNNING")
			nic := stored()
			nic.IP = tc.panel
			nic.Primary = true
			nic.Gateway = tc.gateway
			p.has(nic)

			resp := lnaRead(t, c, lnaStored(t, nil))

			lnaOnly(t, resp.Diagnostics, nil, []string{lnaSummaryDrift})
			if m := lnaModel(t, resp.State); m.IP.ValueString() != lnaAddress {
				t.Errorf("ip = %v, want %s: Read must keep the address of the state", m.IP, lnaAddress)
			}
			detail := diagDetail(t, resp.Diagnostics, lnaSummaryDrift)
			wantInText(t, detail, want)
			wantNotInText(t, detail, "move the gateway", "No gateway", "state rm", "terraform import", "apply -replace", "set ip to it")
		})
	}
}

func TestLocalNetworkAttachmentRead_AnAddressNotRecordedYetIsTakenFromThePanel(t *testing.T) {
	c, p := newNICPanel(t, "RUNNING")
	p.has(stored())

	resp := lnaRead(t, c, lnaStored(t, map[string]tftypes.Value{"ip": lnaNull(tftypes.String)}))

	lnaOnly(t, resp.Diagnostics, nil, nil)
	if m := lnaModel(t, resp.State); m.IP.ValueString() != lnaAddress {
		t.Errorf("ip = %v, want %s", m.IP, lnaAddress)
	}
}

// ---- Update ----

func TestLocalNetworkAttachmentUpdate_OnlyRecordsTheNewOptions(t *testing.T) {
	c, p := newNICPanel(t, "RUNNING")
	prior := lnaStored(t, nil)
	planned := lnaStored(t, map[string]tftypes.Value{
		"reboot_after_attach": lnaFlag("true"),
		"timeouts":            lnaTimeouts(t, "30m", "20m"),
	})

	resp := &resource.UpdateResponse{State: prior}
	lnaResource(c).Update(context.Background(), resource.UpdateRequest{Plan: tfsdk.Plan(planned), State: prior}, resp)

	lnaOnly(t, resp.Diagnostics, nil, nil)
	// Changing when to restart must not touch the VM, nor even talk to the panel.
	wantCalls(t, p.nicCalls(), nil)
	wantCalls(t, p.vp.posts(), nil)
	m := lnaModel(t, resp.State)
	if !m.RebootAfterAttach.ValueBool() {
		t.Errorf("reboot_after_attach = %v, want true", m.RebootAfterAttach)
	}
	if got, diags := m.Timeouts.Create(context.Background(), 0); diags.HasError() || got != 30*time.Minute {
		t.Errorf("stored create timeout = %v (%v), want 30m", got, diags)
	}
	if m.InterfaceID.ValueInt64() != lnaInterfaceID || m.IP.ValueString() != lnaAddress || m.NetworkID.ValueInt64() != lnaNetworkID {
		t.Errorf("Update changed the identity of the attachment: %+v", m)
	}
}

// ---- Delete ----

func TestLocalNetworkAttachmentDelete_DetachesTheInterfaceByItsIDAndLeavesTheVMAlone(t *testing.T) {
	c, p := newNICPanel(t, "RUNNING")
	p.has(stored())

	resp := lnaDelete(t, c, lnaStored(t, nil))

	lnaOnly(t, resp.Diagnostics, nil, nil)
	wantCalls(t, p.nicCalls(), []string{"detach 9071"})
	if got, want := p.nicQueries(), []string{"projectTag=proj&region=TEST"}; !slices.Equal(got, want) {
		t.Errorf("queries = %v, want %v", got, want)
	}
	// Neither a stop nor a start: the detach never restarts the VM.
	wantCalls(t, p.vp.posts(), nil)
	if got := p.vp.current(); got != "RUNNING" {
		t.Errorf("the VM is %s, want it left RUNNING", got)
	}
	if len(p.listed()) != 0 {
		t.Errorf("the VM still has %d interfaces", len(p.listed()))
	}
}

func TestLocalNetworkAttachmentDelete_WhatIsAlreadyGoneIsDone(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    answer
	}{
		{"no such interface, as the test stand says", answer{http.StatusNotFound, 703}},
		{"no such interface, as the other stands say", answer{http.StatusBadRequest, 703}},
		{"no such VM, as the test stand says", answer{http.StatusNotFound, 633}},
		{"no such VM, answered as a bad request", answer{http.StatusBadRequest, 633}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := newNICPanel(t, "RUNNING")
			p.failEvery("detach", tc.a)

			resp := lnaDelete(t, c, lnaStored(t, nil))

			lnaOnly(t, resp.Diagnostics, nil, nil)
			wantCalls(t, p.nicCalls(), []string{"detach 9071"})
		})
	}
}

func TestLocalNetworkAttachmentDelete_ABlockedUserDoesNotHaveItsInterfaceReportedGone(t *testing.T) {
	c, p := newNICPanel(t, "RUNNING")
	p.has(stored())
	p.failEvery("detach", answer{http.StatusForbidden, 601})

	resp := lnaDelete(t, c, lnaStored(t, nil))

	lnaOnly(t, resp.Diagnostics, []string{"Unable to Detach Local Network"}, nil)
	wantInText(t, lnaDetail(t, resp.Diagnostics), "[601]")
}

func TestLocalNetworkAttachmentDelete_AVMThatIsBusyIsAskedAgain(t *testing.T) {
	c, p := newNICPanel(t, "RUNNING")
	p.has(stored())
	p.failNext("detach", answer{http.StatusServiceUnavailable, 961})

	resp := lnaDelete(t, c, lnaStored(t, nil))

	lnaOnly(t, resp.Diagnostics, nil, nil)
	wantCalls(t, p.nicCalls(), []string{"detach 9071", "detach 9071"})
}

func TestLocalNetworkAttachmentDelete_WaitsForAVMToSettleAndNeverStopsIt(t *testing.T) {
	for _, status := range []string{"STOPPING", "STARTING", "REBOOTING"} {
		t.Run(status, func(t *testing.T) {
			c, p := newNICPanel(t, status)
			p.has(stored())
			p.failNext("detach", answer{http.StatusConflict, 959})
			p.vp.setScript(status, status, status, "STOPPED")

			resp := lnaDelete(t, c, lnaStored(t, nil))

			lnaOnly(t, resp.Diagnostics, nil, nil)
			wantCalls(t, p.nicCalls(), []string{"detach 9071", "detach 9071"})
			wantCalls(t, p.vp.posts(), nil)
			if len(p.listed()) != 0 {
				t.Error("the interface is still attached")
			}
		})
	}
}

func TestLocalNetworkAttachmentDelete_AVMThatIsGoneWhileWaitingMeansTheInterfaceIsGone(t *testing.T) {
	// The status endpoint says 601 of a VM that is not there, which is also what a blocked user is
	// told, so it proves nothing; the list of the VM's interfaces says 633, and that is asked once
	// the status cannot be read.
	c, p := newNICPanel(t, "STOPPING")
	p.failNext("detach", answer{http.StatusConflict, 959})
	p.vp.failReadsWith(answer{http.StatusForbidden, 601})
	p.vp.failReadsLike(slices.Repeat([]bool{true}, 50)...)
	p.failEvery("list", answer{http.StatusNotFound, 633})

	resp := lnaDelete(t, c, lnaStored(t, nil))

	lnaOnly(t, resp.Diagnostics, nil, nil)
	wantCalls(t, p.nicCalls(), []string{"detach 9071", "list"})
}

func TestLocalNetworkAttachmentDelete_AStatusThatCannotBeReadOfAVMThatIsStillThereIsNotTakenForGone(t *testing.T) {
	// A blocked user is told 601 by the status endpoint and by the list alike.
	c, p := newNICPanel(t, "STOPPING")
	p.failNext("detach", answer{http.StatusConflict, 959})
	p.vp.failReadsWith(answer{http.StatusForbidden, 601})
	p.vp.failReadsLike(slices.Repeat([]bool{true}, 50)...)
	p.failEvery("list", answer{http.StatusForbidden, 601})

	resp := lnaDelete(t, c, lnaStored(t, nil))

	lnaOnly(t, resp.Diagnostics, []string{"Unable to Detach Local Network"}, nil)
	wantInText(t, lnaDetail(t, resp.Diagnostics), "[959]", "reading the status of VM 7", "3 consecutive failures")
	wantCalls(t, p.nicCalls(), []string{"detach 9071", "list"})
}

func TestLocalNetworkAttachmentDelete_AStatusThatCannotBeReadOfAVMInAnotherProjectIsNotTakenForGone(t *testing.T) {
	// The VM lives in the project the resource names, not in the provider's. The list asked when
	// the status cannot be read has to be asked there as well: in the provider's project it answers
	// 633, and the interface that is still on the VM would be reported detached.
	c, p := newNICPanel(t, "STOPPING")
	p.inProject("other")
	p.has(stored())
	p.failNext("detach", answer{http.StatusConflict, 959})
	p.vp.failReadsWith(answer{http.StatusForbidden, 601})
	p.vp.failReadsLike(slices.Repeat([]bool{true}, 50)...)

	resp := lnaDelete(t, c, lnaStored(t, map[string]tftypes.Value{"project_tag": nirStr("other")}))

	lnaOnly(t, resp.Diagnostics, []string{"Unable to Detach Local Network"}, nil)
	wantInText(t, lnaDetail(t, resp.Diagnostics), "[959]", "reading the status of VM 7", "3 consecutive failures")
	wantCalls(t, p.nicCalls(), []string{"detach 9071", "list"})
	if len(p.listed()) != 1 {
		t.Errorf("the VM has %d interfaces, want the one that was not detached", len(p.listed()))
	}
}

func TestLocalNetworkAttachmentDelete_ANotFoundPageIsNotAnInterfaceThatIsGone(t *testing.T) {
	// A panel that does not have the routes yet answers like this. Calling it a success would leave
	// the interface on the VM and drop it from the state.
	c, p := newNICPanel(t, "RUNNING")
	p.has(stored())
	p.failEvery("detach", answer{http.StatusNotFound, 0})

	resp := lnaDelete(t, c, lnaStored(t, nil))

	lnaOnly(t, resp.Diagnostics, []string{"Unable to Detach Local Network"}, nil)
	wantInText(t, lnaDetail(t, resp.Diagnostics), "404")
	wantCalls(t, p.nicCalls(), []string{"detach 9071"})
}

func TestLocalNetworkAttachmentDelete_ADeleteTimeoutBoundsTheWaitForAVM(t *testing.T) {
	c, p := newNICPanel(t, "STOPPING")
	p.has(stored())
	p.failEvery("detach", answer{http.StatusConflict, 959})
	p.vp.setScript("STOPPING", slices.Repeat([]string{"STOPPING"}, 100000)...)

	start := time.Now()
	resp := lnaDelete(t, c, lnaStored(t, map[string]tftypes.Value{"timeouts": lnaTimeouts(t, "", "150ms")}))

	lnaOnly(t, resp.Diagnostics, []string{"Unable to Detach Local Network"}, nil)
	wantInText(t, lnaDetail(t, resp.Diagnostics), "[959]", "timed out", "STOPPING")
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Delete took %v with a delete timeout of 150ms", elapsed)
	}
	wantCalls(t, p.vp.posts(), nil)
}

func TestLocalNetworkAttachmentDelete_APrimaryInterfaceIsExplained(t *testing.T) {
	c, p := newNICPanel(t, "RUNNING")
	p.has(stored())
	p.failEvery("detach", answer{http.StatusConflict, 958})

	resp := lnaDelete(t, c, lnaStored(t, nil))

	lnaOnly(t, resp.Diagnostics, []string{"Unable to Detach Local Network"}, nil)
	wantInText(t, lnaDetail(t, resp.Diagnostics), "[958]",
		"Interface 9071 of VM 7 has become the VM's primary interface, for example because a default gateway was set on it, "+
			"or its address was edited, in the console, and the platform does not detach a primary interface. To detach it, "+lnaMoveGateway)
	wantCalls(t, p.nicCalls(), []string{"detach 9071"})
}

func TestLocalNetworkAttachmentDelete_ThePlatformCatchAllIsExplainedAndNotMadeWorse(t *testing.T) {
	c, p := newNICPanel(t, "RUNNING")
	p.has(stored())
	p.failEvery("detach", panelRefusal)

	resp := lnaDelete(t, c, lnaStored(t, nil))

	lnaOnly(t, resp.Diagnostics, []string{"Unable to Detach Local Network"}, nil)
	text := lnaDetail(t, resp.Diagnostics)
	wantInText(t, text, "[627]",
		// The first cause costs nothing, and the text says so; the platform may have done part of the removal already.
		"did not finish removing the interface", "Repeating it is harmless", "costs the VM no downtime",
		"could not reach Proxmox", "hotplug", "a reboot from inside the guest is not enough",
		// A change that is only queued is cured by a restart; so is an interface that was waiting for one to appear.
		"stopped and started again", "waiting for a restart to appear", "stop and start the VM once more",
		// The mix-up of interfaces is a question for support only once restarts have not helped.
		"Only if the destroy still fails after that", "Contact support", "console may detach another one",
		// A second restart is asked for only of a VM that was not restarted since the attach; any other goes on.
		"had not been restarted since", "If that does not describe the VM, go on to the next step", lnaSecondRestart,
		// Dropping the attachment from the state does not cancel what is queued.
		"terraform state rm")
	// What costs nothing comes first, support comes last, and the warning against dropping the attachment closes the text.
	wantInOrder(t, text, "run terraform destroy again", "stopped and started again", "waiting for a restart to appear",
		"If that does not describe the VM, go on to the next step", "Contact support", "terraform state rm")
	// The change that is queued may as well be an addition, so "the queued removal" is untrue, and so are
	// "nothing was changed" and "still attached, safe to repeat".
	wantNotInText(t, text, "nothing else was changed", "safe to repeat", "still attached", "queued removal", "whatever is done with this resource",
		"The platform did not remove the interface")
	wantCalls(t, p.nicCalls(), []string{"detach 9071"})
	wantCalls(t, p.vp.posts(), nil)
}

func TestLocalNetworkAttachmentDelete_AnyOtherRefusalIsShown(t *testing.T) {
	c, p := newNICPanel(t, "RUNNING")
	p.failEvery("detach", answer{http.StatusForbidden, 714})

	resp := lnaDelete(t, c, lnaStored(t, nil))

	lnaOnly(t, resp.Diagnostics, []string{"Unable to Detach Local Network"}, nil)
	text := lnaDetail(t, resp.Diagnostics)
	wantInText(t, text, "[714]")
	wantNotInText(t, text, "contact support", "primary interface")
}

// ---- ImportState ----

func TestLocalNetworkAttachmentImportState_SetsTheIdentityAndTheProviderScope(t *testing.T) {
	c, p := newNICPanel(t, "RUNNING")
	p.has(stored())

	resp := lnaImport(t, c, "7:9071")

	lnaOnly(t, resp.Diagnostics, nil, nil)
	wantCalls(t, p.nicCalls(), []string{"list"})
	m := lnaModel(t, resp.State)
	if m.VmID.ValueInt64() != 7 || m.InterfaceID.ValueInt64() != lnaInterfaceID {
		t.Errorf("vm_id/interface_id = %v/%v, want 7/%d", m.VmID, m.InterfaceID, lnaInterfaceID)
	}
	if m.Region.ValueString() != "TEST" || m.ProjectTag.ValueString() != "proj" {
		t.Errorf("region/project_tag = %v/%v, want the provider's, TEST/proj", m.Region, m.ProjectTag)
	}
	// Read, which runs right after, fills these in.
	if !m.NetworkID.IsNull() || !m.IP.IsNull() || !m.RebootAfterAttach.IsNull() {
		t.Errorf("network_id/ip/reboot_after_attach = %v/%v/%v, want all null", m.NetworkID, m.IP, m.RebootAfterAttach)
	}
}

func TestLocalNetworkAttachmentImportState_APrimaryInterfaceIsRefused(t *testing.T) {
	// The advice on the gateway comes only with a gateway: the VM's own interface has none to move. Without a gateway
	// the interface is the VM's own, and the advice to move the VM is given as it is; with one it may be a second
	// interface that only carries the gateway, and a VM with two of those is refused the move: the advice is given for
	// the case that the interface is the VM's own, and says what the move needs.
	const (
		primary = "Interface 9071 is the primary interface of VM 7: the one in the VM's own network, or one with a default gateway. " +
			"It cannot be detached, so it is not managed by this resource."
		moveTheVM = "To put the VM itself in another network, do not detach its main interface: change local_network_id of its prodata_vm, " +
			"which moves the interface in place (see \"Moving a VM to another network\" in the documentation of prodata_vm)."
		moveTheVMIfOwn = "If it is the VM's own interface and the VM is to be in another network, do not detach it: change local_network_id of its " +
			"prodata_vm, which moves the interface in place, as long as no other interface of the VM carries a gateway " +
			"(see \"Moving a VM to another network\" in the documentation of prodata_vm)."
	)
	for _, tc := range []struct {
		name    string
		gateway *string
		want    []string
		wantNot []string
	}{
		{"none recorded", nil, []string{primary + " " + moveTheVM},
			[]string{"move the gateway", "No gateway", "Import it then", "If it is the VM's own interface"}},
		{"an empty one recorded", ptr(""), []string{primary + " " + moveTheVM},
			[]string{"move the gateway", "No gateway", "Import it then", "If it is the VM's own interface"}},
		{"one recorded", ptr("192.168.20.1"),
			[]string{primary + " If it is primary only because it carries the gateway of the VM, " + lnaMoveGateway + " Import it then. " + moveTheVMIfOwn},
			[]string{moveTheVM}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := newNICPanel(t, "RUNNING")
			main := stored()
			main.Primary = true
			main.Gateway = tc.gateway
			p.has(main)

			resp := lnaImport(t, c, "7:9071")

			lnaOnly(t, resp.Diagnostics, []string{"Primary Interface Cannot Be Imported"}, nil)
			detail := lnaDetail(t, resp.Diagnostics)
			wantInText(t, detail, tc.want...)
			wantNotInText(t, detail, tc.wantNot...)
			if !resp.State.Raw.IsNull() {
				t.Error("a primary interface was imported")
			}
		})
	}
}

func TestLocalNetworkAttachmentImportState_AnInterfaceTheVMDoesNotHaveIsRefused(t *testing.T) {
	c, p := newNICPanel(t, "RUNNING")
	p.has(stored())

	resp := lnaImport(t, c, "7:9072")

	lnaOnly(t, resp.Diagnostics, []string{"Interface Not Found"}, nil)
	wantInText(t, lnaDetail(t, resp.Diagnostics), "not of the network")
}

func TestLocalNetworkAttachmentImportState_AVMOutsideTheProvidersProjectIsRefusedAndExplained(t *testing.T) {
	c, p := newNICPanel(t, "RUNNING")
	p.failEvery("list", answer{http.StatusNotFound, 633})

	resp := lnaImport(t, c, "7:9071")

	lnaOnly(t, resp.Diagnostics, []string{"Unable to List Local Network Interfaces"}, nil)
	wantInText(t, lnaDetail(t, resp.Diagnostics), "[633]", "region and project the provider is configured for")
}

func TestLocalNetworkAttachmentImportState_ARefusedListIsShown(t *testing.T) {
	c, p := newNICPanel(t, "RUNNING")
	p.failEvery("list", panelRefusal)

	resp := lnaImport(t, c, "7:9071")

	lnaOnly(t, resp.Diagnostics, []string{"Unable to List Local Network Interfaces"}, nil)
	wantNotInText(t, lnaDetail(t, resp.Diagnostics), "region and project the provider is configured for")
}

func TestLocalNetworkAttachmentImportState_MalformedIDs(t *testing.T) {
	for _, id := range []string{"", "7", "7:", ":9071", "x:9071", "7:y", "7:9071:1"} {
		t.Run(id, func(t *testing.T) {
			c, p := newNICPanel(t, "RUNNING")

			resp := lnaImport(t, c, id)

			lnaOnly(t, resp.Diagnostics, []string{"Invalid Import ID"}, nil)
			wantCalls(t, p.nicCalls(), nil)
			wantInText(t, lnaDetail(t, resp.Diagnostics), "<vm_id>:<interface_id>")
		})
	}
}

// ---- schema ----

func TestLocalNetworkAttachmentPollInterval(t *testing.T) {
	if got := (&LocalNetworkAttachmentResource{}).pollInterval(); got != vmRestartPollInterval {
		t.Errorf("default poll interval = %v, want %v", got, vmRestartPollInterval)
	}
	if got := (&LocalNetworkAttachmentResource{restartPoll: 3 * time.Millisecond}).pollInterval(); got != 3*time.Millisecond {
		t.Errorf("poll interval = %v, want the one set, 3ms", got)
	}
}

func TestLocalNetworkAttachmentDefaultTimeoutsAreTheDocumentedOnes(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "resources", "local_network_attachment.md"))
	if err != nil {
		t.Fatalf("reading the docs: %v", err)
	}
	for key, want := range map[string]time.Duration{
		"create": localNetworkAttachmentDefaultCreateTime,
		"delete": localNetworkAttachmentDefaultDeleteTime,
	} {
		m := regexp.MustCompile("(?m)^- `" + key + "` \\(String\\) Default `([0-9a-z]+)`").FindSubmatch(doc)
		if m == nil {
			t.Errorf("the docs do not state a default for the %s timeout", key)
			continue
		}
		got, err := time.ParseDuration(string(m[1]))
		if err != nil || got != want {
			t.Errorf("the docs say the %s timeout defaults to %s, the code uses %v", key, m[1], want)
		}
	}
}

func TestLocalNetworkAttachmentTheLookupAfterAFailedAttachIsBounded(t *testing.T) {
	// A panel that does not answer must not hold the apply for long after the attach has failed.
	if d := localNetworkAttachmentLookupTimeout; d <= 0 || d > time.Minute {
		t.Errorf("the default bound of the lookup is %v, want something between 0 and a minute", d)
	}
}

func TestLocalNetworkAttachmentLookupLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  time.Duration
		want time.Duration
	}{
		{"not set", 0, 20 * time.Second},
		{"a negative one", -time.Second, 20 * time.Second},
		{"one set by a test", 3 * time.Millisecond, 3 * time.Millisecond},
	} {
		if got := (&LocalNetworkAttachmentResource{lookupTimeout: tc.set}).lookupLimit(); got != tc.want {
			t.Errorf("%s: lookup limit = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestLocalNetworkAttachmentTextHelpers(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{"no address to set", forAddress(nil), ""},
		{"an empty address to set", forAddress(ptr("")), ""},
		{"an address to set", forAddress(ptr("192.0.2.1")), " 192.0.2.1"},
		{"no MAC address", orUnknown(nil), "(unknown)"},
		{"an empty MAC address", orUnknown(ptr("")), "(unknown)"},
		{"a MAC address", orUnknown(ptr("BC:24:11:AA:BB:CC")), "BC:24:11:AA:BB:CC"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestLocalNetworkAttachmentAttachOutcomeIsUnknownOnlyWhereNoPanelAnswered(t *testing.T) {
	for _, tc := range []struct {
		name string
		last error
		want bool
	}{
		{"no request was made", nil, false},
		{"a connection that broke", fmt.Errorf("request failed: unexpected EOF"), true},
		{"a deadline that cut the request short", context.DeadlineExceeded, true},
		{"a gateway timeout page", &client.APIError{StatusCode: http.StatusGatewayTimeout}, true},
		{"a bad gateway page", &client.APIError{StatusCode: http.StatusBadGateway}, true},
		{"a bare internal error page", &client.APIError{StatusCode: http.StatusInternalServerError}, true},
		{"a gateway page the error was wrapped around", fmt.Errorf("attaching: %w", &client.APIError{StatusCode: http.StatusBadGateway}), true},
		{"the catch-all of the panel", &client.APIError{StatusCode: http.StatusInternalServerError, Codes: []int{627}}, false},
		{"a VM that is busy", &client.APIError{StatusCode: http.StatusServiceUnavailable, Codes: []int{961}}, false},
		{"a refusal of the panel", &client.APIError{StatusCode: http.StatusConflict, Codes: []int{955}}, false},
		{"a bare client error page", &client.APIError{StatusCode: http.StatusBadRequest}, false},
	} {
		if got := attachOutcomeUnknown(tc.last); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestLocalNetworkAttachmentSchema_WhoSetsWhat(t *testing.T) {
	_, sch := lnaSchema(t)

	for _, tc := range []struct {
		name                         string
		required, optional, computed bool
	}{
		{"vm_id", true, false, false},
		{"network_id", true, false, false},
		{"ip", false, true, true},
		{"region", false, true, true},
		{"project_tag", false, true, true},
		{"interface_id", false, false, true},
		{"mac_address", false, false, true},
		// Not computed: a null in the configuration must stay a null in the plan, or every plan
		// after an import, where nothing records the flag, would show it as a change.
		{"reboot_after_attach", false, true, false},
		{"timeouts", false, true, false},
	} {
		a, ok := sch.Attributes[tc.name]
		if !ok {
			t.Errorf("%s is missing from the schema", tc.name)
			continue
		}
		if a.IsRequired() != tc.required || a.IsOptional() != tc.optional || a.IsComputed() != tc.computed {
			t.Errorf("%s: required/optional/computed = %v/%v/%v, want %v/%v/%v",
				tc.name, a.IsRequired(), a.IsOptional(), a.IsComputed(), tc.required, tc.optional, tc.computed)
		}
	}
	if len(sch.Attributes) != 9 {
		t.Errorf("the schema has %d attributes, want the 9 above", len(sch.Attributes))
	}
}
