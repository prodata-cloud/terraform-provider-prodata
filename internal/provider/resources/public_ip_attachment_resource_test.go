package resources

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Tests for reboot_after_attach: whether Create restarts the VM, what it tells the user when it
// does not, and that nothing else about the resource changed.

const (
	pipPublicIPID   = 42
	pipWarning      = "Public IP Attached, but It Works Only After the VM Restarts"
	pipFailure      = "Public IP Attached, but the VM Could Not Be Restarted"
	pipNotRestarted = "Public IP Attached, but the VM Was Not Restarted"

	// pipCreateCeiling bounds every Create the tests run, whatever the resource does. A Create
	// that waits for a VM that never changes status — one that restarts a VM it should leave
	// alone, one that ignores its own create timeout — would otherwise hang until the test
	// binary's deadline instead of failing the test that caught it. No correct Create comes near
	// it: the tests poll every millisecond.
	pipCreateCeiling = 10 * time.Second
)

func pipSchema(t *testing.T) (tftypes.Object, schema.Schema) {
	t.Helper()
	var resp resource.SchemaResponse
	NewPublicIPAttachmentResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	obj, ok := resp.Schema.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("schema type is not an object")
	}
	return obj, resp.Schema
}

// pipRaw builds an object value with every attribute null except those in set.
func pipRaw(t *testing.T, set map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	obj, _ := pipSchema(t)
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

// pipFlag is a reboot_after_attach value: "true", "false", or anything else for null.
func pipFlag(v string) tftypes.Value {
	switch v {
	case "true":
		return tftypes.NewValue(tftypes.Bool, true)
	case "false":
		return tftypes.NewValue(tftypes.Bool, false)
	}
	return tftypes.NewValue(tftypes.Bool, nil)
}

// pipTimeouts is a timeouts block with create set, or the null block for "".
func pipTimeouts(t *testing.T, create string) tftypes.Value {
	t.Helper()
	obj, _ := pipSchema(t)
	typ, ok := obj.AttributeTypes["timeouts"].(tftypes.Object)
	if !ok {
		t.Fatal("timeouts is not an object attribute")
	}
	if create == "" {
		return tftypes.NewValue(typ, nil)
	}
	vals := make(map[string]tftypes.Value, len(typ.AttributeTypes))
	for k, at := range typ.AttributeTypes {
		vals[k] = tftypes.NewValue(at, nil)
	}
	vals["create"] = nirStr(create)
	return tftypes.NewValue(typ, vals)
}

// pipCreatePlan is the plan Terraform hands Create: what the user set, and unknown for what
// the provider fills in.
func pipCreatePlan(t *testing.T, flag, timeouts string) tfsdk.Plan {
	t.Helper()
	return pipCreatePlanWith(t, flag, timeouts, nil)
}

// pipCreatePlanWith is pipCreatePlan with some attributes replaced.
func pipCreatePlanWith(t *testing.T, flag, timeouts string, override map[string]tftypes.Value) tfsdk.Plan {
	t.Helper()
	_, sch := pipSchema(t)
	set := map[string]tftypes.Value{
		"vm_id":               nirNum(vmPanelVMID),
		"public_ip_id":        nirNum(pipPublicIPID),
		"public_ip":           nirStrUnknown(),
		"region":              nirStrUnknown(),
		"project_tag":         nirStrUnknown(),
		"reboot_after_attach": pipFlag(flag),
		"timeouts":            pipTimeouts(t, timeouts),
	}
	for k, v := range override {
		set[k] = v
	}
	return tfsdk.Plan{Schema: sch, Raw: pipRaw(t, set)}
}

// pipStored is the state of an attachment that has been created.
func pipStored(t *testing.T, flag, timeouts string) tfsdk.State {
	t.Helper()
	_, sch := pipSchema(t)
	return tfsdk.State{Schema: sch, Raw: pipRaw(t, map[string]tftypes.Value{
		"vm_id":               nirNum(vmPanelVMID),
		"public_ip_id":        nirNum(pipPublicIPID),
		"public_ip":           nirStr("198.51.100.1"),
		"region":              nirStr("TEST"),
		"project_tag":         nirStr("proj"),
		"reboot_after_attach": pipFlag(flag),
		"timeouts":            pipTimeouts(t, timeouts),
	})}
}

func pipNullState(t *testing.T) tfsdk.State {
	t.Helper()
	obj, sch := pipSchema(t)
	return tfsdk.State{Schema: sch, Raw: tftypes.NewValue(obj, nil)}
}

func pipResource(c *client.Client) *PublicIPAttachmentResource {
	return &PublicIPAttachmentResource{client: c, restartPoll: time.Millisecond}
}

func pipCreate(t *testing.T, c *client.Client, plan tfsdk.Plan) *resource.CreateResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), pipCreateCeiling)
	defer cancel()
	resp := &resource.CreateResponse{State: pipNullState(t)}
	pipResource(c).Create(ctx, resource.CreateRequest{Plan: plan}, resp)
	return resp
}

// pipModel reads the attachment out of a state the resource wrote.
func pipModel(t *testing.T, state tfsdk.State) PublicIPAttachmentResourceModel {
	t.Helper()
	if state.Raw.IsNull() {
		t.Fatal("the resource wrote no state")
	}
	var m PublicIPAttachmentResourceModel
	if d := state.Get(context.Background(), &m); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	return m
}

// pipCheckFlag asserts the stored reboot_after_attach is what the plan said: "" for null.
func pipCheckFlag(t *testing.T, got types.Bool, want string) {
	t.Helper()
	if want == "" {
		if !got.IsNull() {
			t.Errorf("reboot_after_attach = %v, want null", got)
		}
		return
	}
	if got.IsNull() || got.IsUnknown() || got.ValueBool() != (want == "true") {
		t.Errorf("reboot_after_attach = %v, want %s", got, want)
	}
}

func diagSummaries(diags diag.Diagnostics) (errs, warns []string) {
	for _, d := range diags {
		switch d.Severity() {
		case diag.SeverityError:
			errs = append(errs, d.Summary())
		case diag.SeverityWarning:
			warns = append(warns, d.Summary())
		}
	}
	return errs, warns
}

// diagDetail is the detail of the diagnostic with this summary.
func diagDetail(t *testing.T, diags diag.Diagnostics, summary string) string {
	t.Helper()
	for _, d := range diags {
		if d.Summary() == summary {
			return d.Detail()
		}
	}
	t.Fatalf("no diagnostic %q in %v", summary, diags)
	return ""
}

func (p *vmPanel) attachIP(id int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.publicIPID = id
}

func (p *vmPanel) requestCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

func (p *vmPanel) allQueries() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.queries)
}

func TestPublicIPAttachmentCreate_WithoutTheFlagTheVMIsNotRestarted(t *testing.T) {
	for _, tc := range []struct {
		status   string
		flag     string
		wantWarn bool
	}{
		// Unset: a VM that is up gets the configuration only on its next start, so say so.
		{"RUNNING", "", true},
		{"STARTING", "", true},
		{"REBOOTING", "", true},
		// A VM that is not up reads its configuration when it starts: nothing to warn about.
		{"STOPPED", "", false},
		{"STOPPING", "", false},
		// false means "I restart it myself": no restart, no warning.
		{"RUNNING", "false", false},
		{"STOPPED", "false", false},
	} {
		t.Run(tc.status+"/flag="+tc.flag, func(t *testing.T) {
			c, p := newVMPanel(t, tc.status)

			resp := pipCreate(t, c, pipCreatePlan(t, tc.flag, ""))

			errs, warns := diagSummaries(resp.Diagnostics)
			if len(errs) != 0 {
				t.Fatalf("unexpected errors: %v", resp.Diagnostics)
			}
			if tc.wantWarn {
				if !slices.Equal(warns, []string{pipWarning}) {
					t.Errorf("warnings = %v, want [%q]", warns, pipWarning)
				}
			} else if len(warns) != 0 {
				t.Errorf("unexpected warnings: %v", warns)
			}
			wantCalls(t, p.posts(), []string{"attach"})

			m := pipModel(t, resp.State)
			if m.PublicIP.ValueString() != vmPanelAddress {
				t.Errorf("public_ip = %q, want %q", m.PublicIP.ValueString(), vmPanelAddress)
			}
			pipCheckFlag(t, m.RebootAfterAttach, tc.flag)
		})
	}
}

func TestPublicIPAttachmentCreate_WarningNamesTheWayOut(t *testing.T) {
	c, _ := newVMPanel(t, "RUNNING")

	resp := pipCreate(t, c, pipCreatePlan(t, "", ""))

	detail := diagDetail(t, resp.Diagnostics, pipWarning)
	for _, want := range []string{"VM 7", "RUNNING", "public IP 42", "reboot_after_attach = true", "reboot_after_attach = false", "not a clean shutdown of the guest operating system"} {
		if !strings.Contains(detail, want) {
			t.Errorf("warning does not mention %q:\n%s", want, detail)
		}
	}
}

func TestPublicIPAttachmentCreate_FlagRestartsARunningVM(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")

	resp := pipCreate(t, c, pipCreatePlan(t, "true", ""))

	if len(resp.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	// The attach comes first: the restart is what makes the new configuration take effect.
	wantCalls(t, p.posts(), []string{"attach", "stop", "start"})
	if atStop, atStart := p.statuses(); atStop != "RUNNING" || atStart != "STOPPED" {
		t.Errorf("stopped at %q and started at %q; want stop while RUNNING and start once STOPPED", atStop, atStart)
	}
	if p.status != "RUNNING" {
		t.Errorf("the VM is %s when Create returns, want RUNNING", p.status)
	}

	m := pipModel(t, resp.State)
	if m.PublicIP.ValueString() != vmPanelAddress {
		t.Errorf("public_ip = %q, want %q", m.PublicIP.ValueString(), vmPanelAddress)
	}
	pipCheckFlag(t, m.RebootAfterAttach, "true")
}

func TestPublicIPAttachmentCreate_FlagLeavesAVMThatIsNotRunningAlone(t *testing.T) {
	c, p := newVMPanel(t, "STOPPED")

	resp := pipCreate(t, c, pipCreatePlan(t, "true", ""))

	if len(resp.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	wantCalls(t, p.posts(), []string{"attach"})
	pipCheckFlag(t, pipModel(t, resp.State).RebootAfterAttach, "true")
}

func TestPublicIPAttachmentCreate_FlagWaitsForAVMThatIsStarting(t *testing.T) {
	c, p := newVMPanel(t, "STARTING")
	p.setScript("STARTING", "STARTING", "RUNNING")

	resp := pipCreate(t, c, pipCreatePlan(t, "true", ""))

	if len(resp.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	wantCalls(t, p.posts(), []string{"attach", "stop", "start"})
	if atStop, _ := p.statuses(); atStop != "RUNNING" {
		t.Errorf("stopped at %q, want RUNNING: a VM still starting is left to finish first", atStop)
	}
}

func TestPublicIPAttachmentCreate_RestartIsScopedLikeTheAttach(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")

	resp := pipCreate(t, c, pipCreatePlanWith(t, "true", "", map[string]tftypes.Value{
		"region":      nirStr("OTHER"),
		"project_tag": nirStr("other-proj"),
	}))

	if len(resp.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	wantCalls(t, p.posts(), []string{"attach", "stop", "start"})
	// The VM is in the attachment's region and project, not the provider's: every call that
	// follows the attach, the status reads included, has to say so.
	for i, q := range p.allQueries() {
		if !strings.Contains(q, "region=OTHER") || !strings.Contains(q, "projectTag=other-proj") {
			t.Errorf("request %d has query %q, want region=OTHER and projectTag=other-proj", i, q)
		}
	}
	m := pipModel(t, resp.State)
	if m.Region.ValueString() != "OTHER" || m.ProjectTag.ValueString() != "other-proj" {
		t.Errorf("stored region/project_tag = %q/%q, want OTHER/other-proj", m.Region.ValueString(), m.ProjectTag.ValueString())
	}
}

func TestPublicIPAttachmentCreate_FailedRestartKeepsTheAttachment(t *testing.T) {
	for _, tc := range []struct {
		name  string
		op    string
		calls []string
	}{
		{"stop fails", "stop", []string{"attach", "stop"}},
		{"start fails", "start", []string{"attach", "stop", "start"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := newVMPanel(t, "RUNNING")
			p.breakCall(tc.op, 403, 403)

			resp := pipCreate(t, c, pipCreatePlan(t, "true", ""))

			errs, warns := diagSummaries(resp.Diagnostics)
			if !slices.Equal(errs, []string{pipFailure}) {
				t.Fatalf("errors = %v, want [%q]", errs, pipFailure)
			}
			// The failure is the whole story: a warning that the VM "was not restarted" next to it would
			// say it again, and wrongly.
			if len(warns) != 0 {
				t.Errorf("warnings next to the failure: %v", warns)
			}
			wantCalls(t, p.posts(), tc.calls)

			// The attachment exists on the panel, so it has to be in the state: Terraform marks it
			// tainted and the next apply detaches and attaches again, instead of a retry being
			// refused for a VM that already has a public IP.
			m := pipModel(t, resp.State)
			if m.PublicIP.ValueString() != vmPanelAddress {
				t.Errorf("public_ip = %q, want %q", m.PublicIP.ValueString(), vmPanelAddress)
			}
			detail := diagDetail(t, resp.Diagnostics, pipFailure)
			for _, want := range []string{"VM 7", "Public IP 42", "403", "tainted"} {
				if !strings.Contains(detail, want) {
					t.Errorf("error does not mention %q:\n%s", want, detail)
				}
			}
		})
	}
}

func TestPublicIPAttachmentCreate_FailedAttachLeavesNoState(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.breakCall("attach", 500, 627)

	resp := pipCreate(t, c, pipCreatePlan(t, "true", ""))

	errs, _ := diagSummaries(resp.Diagnostics)
	if !slices.Equal(errs, []string{"Unable to Attach Public IP"}) {
		t.Fatalf("errors = %v, want the attach failure", errs)
	}
	wantCalls(t, p.posts(), []string{"attach"})
	if !resp.State.Raw.IsNull() {
		t.Errorf("state was written for an attachment that does not exist: %v", resp.State.Raw)
	}
}

func TestPublicIPAttachmentCreate_CreateTimeoutBoundsTheRestart(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.setSteps([]string{"RUNNING"}, nil) // the stop is accepted, but the VM never stops

	begin := time.Now()
	resp := pipCreate(t, c, pipCreatePlan(t, "true", "300ms"))
	elapsed := time.Since(begin)

	errs, _ := diagSummaries(resp.Diagnostics)
	if !slices.Equal(errs, []string{pipFailure}) {
		t.Fatalf("errors = %v, want [%q]", errs, pipFailure)
	}
	if detail := diagDetail(t, resp.Diagnostics, pipFailure); !strings.Contains(detail, "waiting for VM 7 to stop") {
		t.Errorf("error does not say what timed out:\n%s", detail)
	}
	if elapsed > pipCreateCeiling/2 {
		t.Errorf("Create took %s with a create timeout of 300ms", elapsed)
	}
	wantCalls(t, p.posts(), []string{"attach", "stop"})

	// The timeouts block is stored as the user wrote it, and the attachment is kept.
	m := pipModel(t, resp.State)
	if got, diags := m.Timeouts.Create(context.Background(), 0); diags.HasError() || got != 300*time.Millisecond {
		t.Errorf("stored create timeout = %v (%v), want 300ms", got, diags)
	}
}

// Terraform interrupted (Ctrl-C) while the restart waits: Create ends the wait at once and says that
// it was interrupted, instead of running on until its create timeout.
func TestPublicIPAttachmentCreate_InterruptingTerraformEndsTheRestart(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.setSteps([]string{"RUNNING"}, nil) // the stop is accepted, but the VM never stops

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(100*time.Millisecond, cancel)

	// A short create timeout, so that a Create that ignores the interruption still ends and fails
	// the test below, instead of waiting for the default one.
	resp := &resource.CreateResponse{State: pipNullState(t)}
	begin := time.Now()
	pipResource(c).Create(ctx, resource.CreateRequest{Plan: pipCreatePlan(t, "true", "3s")}, resp)
	elapsed := time.Since(begin)

	errs, _ := diagSummaries(resp.Diagnostics)
	if !slices.Equal(errs, []string{pipFailure}) {
		t.Fatalf("errors = %v, want [%q]", errs, pipFailure)
	}
	if detail := diagDetail(t, resp.Diagnostics, pipFailure); !strings.Contains(detail, "interrupted waiting for VM 7 to stop") {
		t.Errorf("error does not say that the wait was interrupted:\n%s", detail)
	}
	if elapsed > time.Second {
		t.Errorf("Create took %s to end after Terraform was interrupted", elapsed)
	}
}

func TestPublicIPAttachmentUpdate_OnlyRecordsTheNewOptions(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	prior := pipStored(t, "", "")
	planned := pipStored(t, "true", "30m")

	resp := &resource.UpdateResponse{State: prior}
	pipResource(c).Update(context.Background(), resource.UpdateRequest{
		Plan:  tfsdk.Plan(planned),
		State: prior,
	}, resp)

	if len(resp.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	// Changing when to restart must not touch the VM, nor even talk to the panel.
	if n := p.requestCount(); n != 0 {
		t.Errorf("Update made %d requests to the panel, want none", n)
	}
	m := pipModel(t, resp.State)
	pipCheckFlag(t, m.RebootAfterAttach, "true")
	if got, diags := m.Timeouts.Create(context.Background(), 0); diags.HasError() || got != 30*time.Minute {
		t.Errorf("stored create timeout = %v (%v), want 30m", got, diags)
	}
	if m.PublicIP.ValueString() != "198.51.100.1" || m.VmID.ValueInt64() != vmPanelVMID || m.PublicIPID.ValueInt64() != pipPublicIPID {
		t.Errorf("Update changed the identity of the attachment: %+v", m)
	}
}

func TestPublicIPAttachmentRead_KeepsTheOptionsAndRefreshesTheAddress(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.attachIP(pipPublicIPID)
	prior := pipStored(t, "true", "10m")

	resp := &resource.ReadResponse{State: prior}
	pipResource(c).Read(context.Background(), resource.ReadRequest{State: prior}, resp)

	if len(resp.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	wantCalls(t, p.posts(), nil)
	m := pipModel(t, resp.State)
	if m.PublicIP.ValueString() != vmPanelAddress {
		t.Errorf("public_ip = %q, want the address the panel reports, %q", m.PublicIP.ValueString(), vmPanelAddress)
	}
	pipCheckFlag(t, m.RebootAfterAttach, "true")
	if got, diags := m.Timeouts.Create(context.Background(), 0); diags.HasError() || got != 10*time.Minute {
		t.Errorf("stored create timeout = %v (%v), want 10m", got, diags)
	}
}

func TestPublicIPAttachmentRead_DriftRemovesTheResource(t *testing.T) {
	for _, tc := range []struct {
		name string
		ip   int64
	}{
		{"no public IP on the VM", 0},
		{"another public IP on the VM", pipPublicIPID + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := newVMPanel(t, "RUNNING")
			p.attachIP(tc.ip)
			prior := pipStored(t, "true", "")

			resp := &resource.ReadResponse{State: prior}
			pipResource(c).Read(context.Background(), resource.ReadRequest{State: prior}, resp)

			if len(resp.Diagnostics) != 0 {
				t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
			}
			if !resp.State.Raw.IsNull() {
				t.Errorf("the attachment is gone from the VM but is still in the state: %v", resp.State.Raw)
			}
		})
	}
}

func TestPublicIPAttachmentDelete_DetachesWithoutRestartingTheVM(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ip    int64
		calls []string
	}{
		{"the VM carries the managed IP", pipPublicIPID, []string{"detach"}},
		{"the VM carries another IP", pipPublicIPID + 1, nil},
		{"the VM carries none", 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := newVMPanel(t, "RUNNING")
			p.attachIP(tc.ip)
			prior := pipStored(t, "true", "")

			resp := &resource.DeleteResponse{State: prior}
			pipResource(c).Delete(context.Background(), resource.DeleteRequest{State: prior}, resp)

			if len(resp.Diagnostics) != 0 {
				t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
			}
			// Never a stop or a start, whatever the flag says: the panel does not give the VM its
			// local gateway back when the public IP goes, so a restart would only strand it.
			wantCalls(t, p.posts(), tc.calls)
		})
	}
}

func TestPublicIPAttachmentImportState_LeavesTheOptionsUnset(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")

	resp := &resource.ImportStateResponse{State: pipNullState(t)}
	pipResource(c).ImportState(context.Background(), resource.ImportStateRequest{ID: "7:42"}, resp)

	if len(resp.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	if n := p.requestCount(); n != 0 {
		t.Errorf("ImportState made %d requests to the panel, want none", n)
	}
	m := pipModel(t, resp.State)
	if m.VmID.ValueInt64() != vmPanelVMID || m.PublicIPID.ValueInt64() != pipPublicIPID {
		t.Errorf("imported vm_id=%v public_ip_id=%v, want 7 and 42", m.VmID, m.PublicIPID)
	}
	pipCheckFlag(t, m.RebootAfterAttach, "")
	if !m.Timeouts.IsNull() {
		t.Errorf("timeouts = %v, want null", m.Timeouts)
	}
}

func TestPublicIPAttachmentPollInterval(t *testing.T) {
	if got := (&PublicIPAttachmentResource{}).pollInterval(); got != vmRestartPollInterval {
		t.Errorf("default poll interval = %v, want %v", got, vmRestartPollInterval)
	}
	if got := (&PublicIPAttachmentResource{restartPoll: 3 * time.Millisecond}).pollInterval(); got != 3*time.Millisecond {
		t.Errorf("poll interval = %v, want the one set for the resource, 3ms", got)
	}
}

func TestPublicIPAttachmentSchema(t *testing.T) {
	_, sch := pipSchema(t)

	t.Run("reboot_after_attach is a plain optional flag", func(t *testing.T) {
		attr, ok := sch.Attributes["reboot_after_attach"].(schema.BoolAttribute)
		if !ok {
			t.Fatalf("reboot_after_attach is %T, want a bool attribute", sch.Attributes["reboot_after_attach"])
		}
		if !attr.Optional || attr.Required || attr.Computed {
			t.Errorf("optional/required/computed = %v/%v/%v, want true/false/false: unset must stay null, not become false", attr.Optional, attr.Required, attr.Computed)
		}
		if attr.Default != nil {
			t.Error("reboot_after_attach has a default: unset must stay distinguishable from false, which silences the warning")
		}
		if len(attr.PlanModifiers) != 0 {
			t.Errorf("reboot_after_attach has %d plan modifiers: changing it must not replace the attachment, which would detach the public IP", len(attr.PlanModifiers))
		}
	})

	t.Run("the identity of the attachment still forces replacement", func(t *testing.T) {
		// Update no longer fails, so these are all that keeps a change of VM, public IP, region or
		// project from being recorded in the state of an attachment that stays where it was.
		for _, tc := range []struct {
			name     string
			was, now int64
		}{{"vm_id", 7, 8}, {"public_ip_id", 42, 43}} {
			if !pipInt64Replaces(t, tc.name, tc.was, tc.now) {
				t.Errorf("changing %s does not replace the attachment", tc.name)
			}
			if pipInt64Replaces(t, tc.name, tc.was, tc.was) {
				t.Errorf("leaving %s as it is replaces the attachment", tc.name)
			}
		}
		for _, tc := range []struct {
			name     string
			was, now string
		}{{"region", "TEST", "OTHER"}, {"project_tag", "proj", "other-proj"}} {
			if !pipStringReplaces(t, tc.name, tc.was, tc.now) {
				t.Errorf("changing %s does not replace the attachment", tc.name)
			}
			if pipStringReplaces(t, tc.name, tc.was, tc.was) {
				t.Errorf("leaving %s as it is replaces the attachment", tc.name)
			}
		}
	})

	t.Run("the options never force replacement", func(t *testing.T) {
		attr, ok := sch.Attributes["timeouts"].(schema.SingleNestedAttribute)
		if !ok {
			t.Fatalf("timeouts is %T, want a nested attribute", sch.Attributes["timeouts"])
		}
		if len(attr.PlanModifiers) != 0 {
			t.Errorf("timeouts has %d plan modifiers: changing it must not replace the attachment", len(attr.PlanModifiers))
		}
	})

	t.Run("timeouts takes a create timeout", func(t *testing.T) {
		attr, ok := sch.Attributes["timeouts"]
		if !ok || !attr.IsOptional() {
			t.Fatalf("timeouts is %v, want an optional attribute", attr)
		}
	})
}

// pipInt64Replaces runs the plan modifiers of an int64 attribute for a change to an attachment that
// already exists, as the framework does, and reports whether any of them asks for a replacement.
func pipInt64Replaces(t *testing.T, name string, was, now int64) bool {
	t.Helper()
	_, sch := pipSchema(t)
	attr, ok := sch.Attributes[name].(schema.Int64Attribute)
	if !ok {
		t.Fatalf("%s is %T, want an int64 attribute", name, sch.Attributes[name])
	}
	state := pipStored(t, "", "")
	req := planmodifier.Int64Request{
		Path:        path.Root(name),
		Config:      tfsdk.Config(state),
		ConfigValue: types.Int64Value(now),
		Plan:        tfsdk.Plan(state),
		PlanValue:   types.Int64Value(now),
		State:       state,
		StateValue:  types.Int64Value(was),
	}
	resp := &planmodifier.Int64Response{PlanValue: req.PlanValue}
	for _, m := range attr.PlanModifiers {
		m.PlanModifyInt64(context.Background(), req, resp)
		req.PlanValue = resp.PlanValue
	}
	return resp.RequiresReplace
}

// pipStringReplaces is pipInt64Replaces for a string attribute.
func pipStringReplaces(t *testing.T, name string, was, now string) bool {
	t.Helper()
	_, sch := pipSchema(t)
	attr, ok := sch.Attributes[name].(schema.StringAttribute)
	if !ok {
		t.Fatalf("%s is %T, want a string attribute", name, sch.Attributes[name])
	}
	state := pipStored(t, "", "")
	req := planmodifier.StringRequest{
		Path:        path.Root(name),
		Config:      tfsdk.Config(state),
		ConfigValue: types.StringValue(now),
		Plan:        tfsdk.Plan(state),
		PlanValue:   types.StringValue(now),
		State:       state,
		StateValue:  types.StringValue(was),
	}
	resp := &planmodifier.StringResponse{PlanValue: req.PlanValue}
	for _, m := range attr.PlanModifiers {
		m.PlanModifyString(context.Background(), req, resp)
		req.PlanValue = resp.PlanValue
	}
	return resp.RequiresReplace
}

func TestPublicIPAttachmentCreate_FlagWithAVMThatWentDownBeforeTheRestartWarns(t *testing.T) {
	// Up when the public IP is attached, STOPPED by the time the provider looks: somebody stopped the
	// VM in between. It is not the provider's call to start it, but the user has asked for a restart
	// and has to hear that it did not happen, whichever way up the VM was.
	for _, status := range []string{"RUNNING", "STARTING", "REBOOTING"} {
		t.Run(status, func(t *testing.T) {
			c, p := newVMPanel(t, status)
			p.setScript(status, "STOPPED")

			resp := pipCreate(t, c, pipCreatePlan(t, "true", ""))

			errs, warns := diagSummaries(resp.Diagnostics)
			if len(errs) != 0 {
				t.Fatalf("unexpected errors: %v", resp.Diagnostics)
			}
			if !slices.Equal(warns, []string{pipNotRestarted}) {
				t.Errorf("warnings = %v, want [%q]", warns, pipNotRestarted)
			}
			wantCalls(t, p.posts(), []string{"attach"})
			detail := diagDetail(t, resp.Diagnostics, pipNotRestarted)
			for _, want := range []string{"VM 7", status, "public IP 42", "does not start a VM that is not running"} {
				if !strings.Contains(detail, want) {
					t.Errorf("warning does not mention %q:\n%s", want, detail)
				}
			}
			pipCheckFlag(t, pipModel(t, resp.State).RebootAfterAttach, "true")
		})
	}
}

func TestPublicIPAttachmentCreate_FlagRestartsAVMWhoseStopWasAnsweredWithAnError(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	p.loseNextReply("stop") // the panel stops the VM and answers 627 all the same

	resp := pipCreate(t, c, pipCreatePlan(t, "true", ""))

	// The VM is off because of the provider's stop: it is started again, with neither an error nor
	// a warning, rather than left off with a successful apply.
	if len(resp.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	wantCalls(t, p.posts(), []string{"attach", "stop", "start"})
	if p.status != "RUNNING" {
		t.Errorf("the VM is %s when Create returns, want RUNNING", p.status)
	}
}

func TestPublicIPAttachmentCreate_FlagRestartsAVMWhoseStartWasAnsweredWithAGatewayError(t *testing.T) {
	c, p := newVMPanel(t, "RUNNING")
	// The VM is off because of the provider's stop, and the first start meets a gateway that times
	// out: the apply goes on to start the VM instead of failing and leaving it off.
	p.failCalls("start", 1, gatewayTimeout)

	resp := pipCreate(t, c, pipCreatePlan(t, "true", ""))

	if len(resp.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	wantCalls(t, p.posts(), []string{"attach", "stop", "start", "start"})
	if p.status != "RUNNING" {
		t.Errorf("the VM is %s when Create returns, want RUNNING", p.status)
	}
}

func TestVMIsUp(t *testing.T) {
	for status, want := range map[string]bool{
		"RUNNING": true, "STARTING": true, "REBOOTING": true,
		"STOPPED": false, "STOPPING": false, "CREATING": false, "ERROR": false, "DELETED": false, "": false,
	} {
		if got := vmIsUp(status); got != want {
			t.Errorf("vmIsUp(%q) = %v, want %v", status, got, want)
		}
	}
}

func TestPublicIPAttachmentDefaultCreateTimeIsWhatTheDocsPromise(t *testing.T) {
	if publicIPAttachmentDefaultCreateTime != 15*time.Minute {
		t.Errorf("default create timeout = %v, want 15m: the documentation says so", publicIPAttachmentDefaultCreateTime)
	}
}
