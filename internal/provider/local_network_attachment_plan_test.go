package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Protocol-level tests of what Terraform plans for prodata_local_network_attachment. The unit
// tests of the resource call its methods and so cannot see what the framework does between them:
// which attributes Terraform sends for replacement, and which computed values it keeps instead of
// marking unknown. Those are decided by plan modifiers, and a mistake in them is a destroy and
// create of a live network interface (or a plan that never settles) with every method still right.
//
// The provider is driven through the interface Terraform itself uses (PlanResourceChange,
// ReadResource, ImportResourceState), with the panel an httptest server. What Terraform core does
// around the call is done here by hand, as little of it as the question needs: it proposes a new
// state (a computed attribute left out of the configuration keeps its prior value) and replaces the
// resource when an attribute the provider listed as RequiresReplace has a planned value that is not
// the prior one. No Terraform binary, no credentials, no network.

const (
	lnapType    = "prodata_local_network_attachment"
	lnapPath    = "/panel-main/api/v2/vms/7/local-networks"
	lnapAddress = "192.168.20.15"
	lnapMAC     = "BC:24:11:AA:BB:CC"
)

// lnapPanel answers the one request the lifecycle makes of the panel, the list of a VM's local
// network interfaces; anything else is a failure of the test.
type lnapPanel struct {
	srv  *httptest.Server
	mu   sync.Mutex
	nics []map[string]any
}

func newLnapPanel(t *testing.T) *lnapPanel {
	t.Helper()
	p := &lnapPanel{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if r.Method == http.MethodGet && r.URL.Path == lnapPath {
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{"success": true, "data": p.nics, "errors": nil}); err != nil {
				t.Errorf("encoding the list: %v", err)
			}
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

// has puts the interface 9071 of network 412 on the VM, with this address.
func (p *lnapPanel) has(ip string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nics = []map[string]any{{
		"id": 9071, "networkId": 412, "networkName": "backend", "ip": ip, "mac": lnapMAC, "gateway": nil, "primary": false,
	}}
}

type lnapEnv struct {
	t        *testing.T
	s        *ephServer
	typ      tftypes.Object
	computed map[string]bool
	panel    *lnapPanel
}

func newLnapEnv(t *testing.T) *lnapEnv {
	t.Helper()
	s := newEphServer(t)
	sch, ok := s.schema.ResourceSchemas[lnapType]
	if !ok {
		t.Fatalf("resource %q is not registered", lnapType)
	}
	typ, ok := sch.ValueType().(tftypes.Object)
	if !ok {
		t.Fatalf("the type of %q is %T, want an object", lnapType, sch.ValueType())
	}
	computed := map[string]bool{}
	for _, a := range sch.Block.Attributes {
		computed[a.Name] = a.Computed
	}
	panel := newLnapPanel(t)
	s.configure(panel.srv.URL)
	return &lnapEnv{t: t, s: s, typ: typ, computed: computed, panel: panel}
}

func lnapNum(n int64) tftypes.Value { return tftypes.NewValue(tftypes.Number, n) }
func lnapBool(b bool) tftypes.Value { return tftypes.NewValue(tftypes.Bool, b) }

// object is a value of the resource's type with every attribute null except those in set.
func (e *lnapEnv) object(set map[string]tftypes.Value) tftypes.Value {
	e.t.Helper()
	vals := make(map[string]tftypes.Value, len(e.typ.AttributeTypes))
	for k, at := range e.typ.AttributeTypes {
		vals[k] = tftypes.NewValue(at, nil)
	}
	for k, v := range set {
		if _, known := vals[k]; !known {
			e.t.Fatalf("%q is not an attribute of %s", k, lnapType)
		}
		vals[k] = v
	}
	return tftypes.NewValue(e.typ, vals)
}

// timeouts is a timeouts block with create set.
func (e *lnapEnv) timeouts(create string) tftypes.Value {
	e.t.Helper()
	typ, ok := e.typ.AttributeTypes["timeouts"].(tftypes.Object)
	if !ok {
		e.t.Fatal("timeouts is not an object attribute")
	}
	vals := make(map[string]tftypes.Value, len(typ.AttributeTypes))
	for k, at := range typ.AttributeTypes {
		vals[k] = tftypes.NewValue(at, nil)
	}
	vals["create"] = ephStr(create)
	return tftypes.NewValue(typ, vals)
}

// stored is the state of an attachment Create has recorded, with attributes replaced. The provider
// defaults ephServer.configure sets went into region and project_tag.
func (e *lnapEnv) stored(override map[string]tftypes.Value) tftypes.Value {
	set := map[string]tftypes.Value{
		"vm_id":        lnapNum(7),
		"network_id":   lnapNum(412),
		"ip":           ephStr(lnapAddress),
		"interface_id": lnapNum(9071),
		"mac_address":  ephStr(lnapMAC),
		"region":       ephStr("PROV-REGION"),
		"project_tag":  ephStr("prov-project"),
	}
	for k, v := range override {
		set[k] = v
	}
	return e.object(set)
}

// minimal is the configuration of the attachment in the plain case: the VM and the network, and
// nothing the provider can fill in.
func (e *lnapEnv) minimal(extra map[string]tftypes.Value) tftypes.Value {
	set := map[string]tftypes.Value{"vm_id": lnapNum(7), "network_id": lnapNum(412)}
	for k, v := range extra {
		set[k] = v
	}
	return e.object(set)
}

// explicit is a configuration that says everything it can: the address and the scope.
func (e *lnapEnv) explicit(extra map[string]tftypes.Value) tftypes.Value {
	return e.minimal(mergeValues(map[string]tftypes.Value{
		"ip":          ephStr(lnapAddress),
		"region":      ephStr("PROV-REGION"),
		"project_tag": ephStr("prov-project"),
	}, extra))
}

func mergeValues(a, b map[string]tftypes.Value) map[string]tftypes.Value {
	out := make(map[string]tftypes.Value, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func (e *lnapEnv) attrs(v tftypes.Value) map[string]tftypes.Value {
	e.t.Helper()
	m := map[string]tftypes.Value{}
	if v.IsNull() {
		return m
	}
	if err := v.As(&m); err != nil {
		e.t.Fatalf("reading an object value: %v", err)
	}
	return m
}

func (e *lnapEnv) dyn(v tftypes.Value) *tfprotov6.DynamicValue {
	e.t.Helper()
	dv, err := tfprotov6.NewDynamicValue(e.typ, v)
	if err != nil {
		e.t.Fatalf("NewDynamicValue: %v", err)
	}
	return &dv
}

func (e *lnapEnv) decode(dv *tfprotov6.DynamicValue) tftypes.Value {
	e.t.Helper()
	if dv == nil {
		e.t.Fatal("the provider returned no value")
	}
	v, err := dv.Unmarshal(e.typ)
	if err != nil {
		e.t.Fatalf("decoding a value: %v", err)
	}
	return v
}

// proposedNew is what Terraform core hands the provider as the proposed new state: the
// configuration, except that a computed attribute the configuration leaves null keeps its prior
// value (null, on create).
func (e *lnapEnv) proposedNew(prior, config tftypes.Value) tftypes.Value {
	priorAttrs, configAttrs := e.attrs(prior), e.attrs(config)
	out := make(map[string]tftypes.Value, len(e.typ.AttributeTypes))
	for name, at := range e.typ.AttributeTypes {
		switch {
		case !e.computed[name]:
			out[name] = configAttrs[name]
		case !configAttrs[name].IsNull():
			out[name] = configAttrs[name]
		case priorAttrs[name].Type() != nil:
			out[name] = priorAttrs[name]
		default:
			out[name] = tftypes.NewValue(at, nil)
		}
	}
	return tftypes.NewValue(e.typ, out)
}

type lnapPlan struct {
	planned tftypes.Value
	// replace names the attributes that make Terraform destroy and create the resource: the
	// provider's RequiresReplace paths whose planned value is not the prior one.
	replace []string
}

// plan asks the provider to plan the change from prior (null for a create) to config.
func (e *lnapEnv) plan(prior, config tftypes.Value) lnapPlan {
	e.t.Helper()
	resp, err := e.s.srv.PlanResourceChange(context.Background(), &tfprotov6.PlanResourceChangeRequest{
		TypeName:         lnapType,
		PriorState:       e.dyn(prior),
		ProposedNewState: e.dyn(e.proposedNew(prior, config)),
		Config:           e.dyn(config),
	})
	if err != nil {
		e.t.Fatalf("PlanResourceChange: %v", err)
	}
	if d := ephErrors(resp.Diagnostics); d != "" {
		e.t.Fatalf("PlanResourceChange diagnostics: %s", d)
	}
	out := lnapPlan{planned: e.decode(resp.PlannedState)}
	plannedAttrs, priorAttrs := e.attrs(out.planned), e.attrs(prior)
	for _, p := range resp.RequiresReplace {
		steps := p.Steps()
		if len(steps) != 1 {
			e.t.Fatalf("RequiresReplace path %s is not a top-level attribute", p)
		}
		step, ok := steps[0].(tftypes.AttributeName)
		if !ok {
			e.t.Fatalf("RequiresReplace path %s does not start with an attribute name", p)
		}
		name := string(step)
		if prior.IsNull() {
			continue
		}
		if !plannedAttrs[name].Equal(priorAttrs[name]) {
			out.replace = append(out.replace, name)
		}
	}
	slices.Sort(out.replace)
	return out
}

func (e *lnapEnv) read(state tftypes.Value) (tftypes.Value, string) {
	e.t.Helper()
	resp, err := e.s.srv.ReadResource(context.Background(), &tfprotov6.ReadResourceRequest{
		TypeName: lnapType, CurrentState: e.dyn(state),
	})
	if err != nil {
		e.t.Fatalf("ReadResource: %v", err)
	}
	if d := ephErrors(resp.Diagnostics); d != "" {
		e.t.Fatalf("ReadResource diagnostics: %s", d)
	}
	return e.decode(resp.NewState), ephDiagText(resp.Diagnostics)
}

func (e *lnapEnv) importState(id string) tftypes.Value {
	e.t.Helper()
	resp, err := e.s.srv.ImportResourceState(context.Background(), &tfprotov6.ImportResourceStateRequest{TypeName: lnapType, ID: id})
	if err != nil {
		e.t.Fatalf("ImportResourceState: %v", err)
	}
	if d := ephErrors(resp.Diagnostics); d != "" {
		e.t.Fatalf("ImportResourceState diagnostics: %s", d)
	}
	if len(resp.ImportedResources) != 1 {
		e.t.Fatalf("%d resources imported, want 1", len(resp.ImportedResources))
	}
	return e.decode(resp.ImportedResources[0].State)
}

// wantAttrs fails the test unless the object holds these values.
func (e *lnapEnv) wantAttrs(what string, v tftypes.Value, want map[string]tftypes.Value) {
	e.t.Helper()
	got := e.attrs(v)
	for name, w := range want {
		if !got[name].Equal(w) {
			e.t.Errorf("%s: %s = %s, want %s", what, name, got[name], w)
		}
	}
}

func TestProviderServer_LocalNetworkAttachment_PlanOfANewAttachment(t *testing.T) {
	e := newLnapEnv(t)
	null := tftypes.NewValue(e.typ, nil)

	t.Run("what the provider fills in is unknown", func(t *testing.T) {
		p := e.plan(null, e.minimal(nil))

		if len(p.replace) != 0 {
			t.Errorf("a new resource is replaced because of %v", p.replace)
		}
		attrs := e.attrs(p.planned)
		for _, name := range []string{"ip", "interface_id", "mac_address", "region", "project_tag"} {
			if attrs[name].IsKnown() {
				t.Errorf("%s = %s, want unknown: the provider fills it in", name, attrs[name])
			}
		}
		e.wantAttrs("planned", p.planned, map[string]tftypes.Value{"vm_id": lnapNum(7), "network_id": lnapNum(412)})
	})

	t.Run("what the configuration says is kept", func(t *testing.T) {
		p := e.plan(null, e.explicit(map[string]tftypes.Value{"reboot_after_attach": lnapBool(true)}))

		e.wantAttrs("planned", p.planned, map[string]tftypes.Value{
			"ip": ephStr(lnapAddress), "region": ephStr("PROV-REGION"), "project_tag": ephStr("prov-project"),
			"reboot_after_attach": lnapBool(true),
		})
		attrs := e.attrs(p.planned)
		for _, name := range []string{"interface_id", "mac_address"} {
			if attrs[name].IsKnown() {
				t.Errorf("%s = %s, want unknown: the provider fills it in", name, attrs[name])
			}
		}
	})
}

func TestProviderServer_LocalNetworkAttachment_PlanOfAnExistingAttachment(t *testing.T) {
	e := newLnapEnv(t)

	for _, tc := range []struct {
		name  string
		prior map[string]tftypes.Value // changes to the state Create recorded
		// config is the configuration as the user wrote it.
		config tftypes.Value

		wantReplace []string
		// wantChange is what the plan changes in place, by attribute; every other attribute of the
		// state, the computed ones included, must come out of the plan as it was.
		wantChange map[string]tftypes.Value
	}{
		// Nothing changes.
		{name: "the same minimal configuration", config: e.minimal(nil)},
		{name: "the same explicit configuration", config: e.explicit(nil)},
		{name: "the address written out as the platform gave it", config: e.minimal(map[string]tftypes.Value{"ip": ephStr(lnapAddress)})},

		// The options of Create and Delete change in place, with no replacement and no unknown left
		// behind: a plan modifier missing from a computed attribute shows up here, because the
		// framework marks a computed attribute unknown as soon as the plan has any change.
		{
			name:       "the restart flag set",
			config:     e.minimal(map[string]tftypes.Value{"reboot_after_attach": lnapBool(true)}),
			wantChange: map[string]tftypes.Value{"reboot_after_attach": lnapBool(true)},
		},
		{
			name:       "the restart flag set to false",
			config:     e.minimal(map[string]tftypes.Value{"reboot_after_attach": lnapBool(false)}),
			wantChange: map[string]tftypes.Value{"reboot_after_attach": lnapBool(false)},
		},
		{
			name:       "the restart flag set on an explicit configuration",
			config:     e.explicit(map[string]tftypes.Value{"reboot_after_attach": lnapBool(true)}),
			wantChange: map[string]tftypes.Value{"reboot_after_attach": lnapBool(true)},
		},
		{
			name:       "the restart flag taken out",
			prior:      map[string]tftypes.Value{"reboot_after_attach": lnapBool(true)},
			config:     e.minimal(nil),
			wantChange: map[string]tftypes.Value{"reboot_after_attach": tftypes.NewValue(tftypes.Bool, nil)},
		},
		{
			name:       "a timeout set",
			config:     e.minimal(map[string]tftypes.Value{"timeouts": e.timeouts("30m")}),
			wantChange: map[string]tftypes.Value{"timeouts": e.timeouts("30m")},
		},

		// What identifies the attachment replaces it.
		{name: "another VM", config: e.minimal(map[string]tftypes.Value{"vm_id": lnapNum(8)}), wantReplace: []string{"vm_id"}, wantChange: map[string]tftypes.Value{"vm_id": lnapNum(8)}},
		{name: "another network", config: e.minimal(map[string]tftypes.Value{"network_id": lnapNum(413)}), wantReplace: []string{"network_id"}, wantChange: map[string]tftypes.Value{"network_id": lnapNum(413)}},
		{name: "another address", config: e.minimal(map[string]tftypes.Value{"ip": ephStr("192.168.20.16")}), wantReplace: []string{"ip"}, wantChange: map[string]tftypes.Value{"ip": ephStr("192.168.20.16")}},
		{name: "another region", config: e.minimal(map[string]tftypes.Value{"region": ephStr("OTHER")}), wantReplace: []string{"region"}, wantChange: map[string]tftypes.Value{"region": ephStr("OTHER")}},
		{name: "another project", config: e.minimal(map[string]tftypes.Value{"project_tag": ephStr("other")}), wantReplace: []string{"project_tag"}, wantChange: map[string]tftypes.Value{"project_tag": ephStr("other")}},
		{
			name:        "another address and the restart flag at once",
			config:      e.minimal(map[string]tftypes.Value{"ip": ephStr("192.168.20.16"), "reboot_after_attach": lnapBool(true)}),
			wantReplace: []string{"ip"},
			wantChange:  map[string]tftypes.Value{"ip": ephStr("192.168.20.16"), "reboot_after_attach": lnapBool(true)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prior := e.stored(tc.prior)

			p := e.plan(prior, tc.config)

			if !slices.Equal(p.replace, tc.wantReplace) {
				t.Errorf("replaced because of %v, want %v", p.replace, tc.wantReplace)
			}
			planned, priorAttrs := e.attrs(p.planned), e.attrs(prior)
			for name, v := range tc.wantChange {
				if !planned[name].Equal(v) {
					t.Errorf("planned %s = %s, want %s", name, planned[name], v)
				}
			}
			if len(tc.wantReplace) > 0 {
				// A replacement is planned again from nothing; the computed values of the old
				// attachment are not carried over.
				return
			}
			for name := range e.typ.AttributeTypes {
				if _, changed := tc.wantChange[name]; changed {
					continue
				}
				if !planned[name].Equal(priorAttrs[name]) {
					t.Errorf("planned %s = %s, want it left as it is, %s", name, planned[name], priorAttrs[name])
				}
			}
			if len(tc.wantChange) == 0 && !p.planned.Equal(prior) {
				t.Errorf("the plan is not empty: %s", p.planned)
			}
		})
	}
}

// Read keeps the address the state has, whatever the panel says (it cannot see the configuration),
// and warns instead. Were it to take the panel's address, a configuration that names its own would
// plan a replacement of the interface: a detach and an attach of a live network interface for an
// address somebody changed on purpose.
func TestProviderServer_LocalNetworkAttachment_AnExplicitAddressChangedInThePanelKeepsThePlanEmpty(t *testing.T) {
	e := newLnapEnv(t)
	e.panel.has("192.168.20.99")
	prior := e.stored(nil)

	refreshed, diags := e.read(prior)

	if !strings.Contains(diags, "Local Network Interface Address Changed Outside Terraform") {
		t.Errorf("Read did not warn about the address:\n%s", diags)
	}
	if !refreshed.Equal(prior) {
		t.Fatalf("Read changed the state:\n%s\nwas:\n%s", refreshed, prior)
	}
	for name, config := range map[string]tftypes.Value{"minimal": e.minimal(nil), "explicit": e.explicit(nil)} {
		p := e.plan(refreshed, config)
		if len(p.replace) != 0 || !p.planned.Equal(refreshed) {
			t.Errorf("%s configuration: replace = %v, planned:\n%s\nwant an empty plan", name, p.replace, p.planned)
		}
	}
}

// An imported attachment has nothing of the configuration in its state, only what Read filled in.
// The configuration that describes it must plan no change at all.
func TestProviderServer_LocalNetworkAttachment_ImportedAttachmentPlansNoChange(t *testing.T) {
	e := newLnapEnv(t)
	e.panel.has(lnapAddress)

	imported := e.importState("7:9071")
	e.wantAttrs("imported", imported, map[string]tftypes.Value{
		"vm_id": lnapNum(7), "interface_id": lnapNum(9071), "region": ephStr("PROV-REGION"), "project_tag": ephStr("prov-project"),
	})
	state, diags := e.read(imported)
	if diags != "" {
		t.Errorf("Read after the import said something:\n%s", diags)
	}
	e.wantAttrs("read", state, map[string]tftypes.Value{
		"vm_id": lnapNum(7), "network_id": lnapNum(412), "ip": ephStr(lnapAddress), "interface_id": lnapNum(9071),
		"mac_address": ephStr(lnapMAC), "region": ephStr("PROV-REGION"), "project_tag": ephStr("prov-project"),
	})
	attrs := e.attrs(state)
	if !attrs["reboot_after_attach"].IsNull() || !attrs["timeouts"].IsNull() {
		t.Errorf("reboot_after_attach/timeouts = %s/%s, want both null: nothing records them", attrs["reboot_after_attach"], attrs["timeouts"])
	}

	for _, tc := range []struct {
		name        string
		config      tftypes.Value
		wantEmpty   bool
		wantReplace []string
	}{
		{name: "the minimal configuration", config: e.minimal(nil), wantEmpty: true},
		{name: "the explicit configuration", config: e.explicit(nil), wantEmpty: true},
		{name: "a restart flag", config: e.minimal(map[string]tftypes.Value{"reboot_after_attach": lnapBool(false)})},
		{name: "another address", config: e.minimal(map[string]tftypes.Value{"ip": ephStr("192.168.20.16")}), wantReplace: []string{"ip"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := e.plan(state, tc.config)

			if !slices.Equal(p.replace, tc.wantReplace) {
				t.Errorf("replaced because of %v, want %v", p.replace, tc.wantReplace)
			}
			if tc.wantEmpty && !p.planned.Equal(state) {
				t.Errorf("the plan is not empty:\n%s\nstate:\n%s", p.planned, state)
			}
		})
	}
}
