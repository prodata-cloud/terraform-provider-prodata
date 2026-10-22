package resources

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// vmPlanProvider serves only prodata_vm, so that a plan goes through the framework the way it does
// under Terraform: the unknown marking of computed attributes, the plan modifiers of the
// attributes, then ModifyPlan.
type vmPlanProvider struct{}

func (vmPlanProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "prodata"
}
func (vmPlanProvider) Schema(context.Context, provider.SchemaRequest, *provider.SchemaResponse) {}
func (vmPlanProvider) Configure(context.Context, provider.ConfigureRequest, *provider.ConfigureResponse) {
}
func (vmPlanProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{NewVmResource}
}
func (vmPlanProvider) DataSources(context.Context) []func() datasource.DataSource { return nil }

// vmValues is a prodata_vm as Terraform hands it over: attribute name to value.
type vmValues map[string]tftypes.Value

func (v vmValues) with(name string, value tftypes.Value) vmValues {
	out := make(vmValues, len(v)+1)
	for k, x := range v {
		out[k] = x
	}
	out[name] = value
	return out
}

var (
	vmStr    = func(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }
	vmNum    = func(n int64) tftypes.Value { return tftypes.NewValue(tftypes.Number, n) }
	vmNullS  = tftypes.NewValue(tftypes.String, nil)
	vmNullN  = tftypes.NewValue(tftypes.Number, nil)
	vmUnkS   = tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
	vmUnkN   = tftypes.NewValue(tftypes.Number, tftypes.UnknownValue)
	vmNetOld = int64(10)
	vmNetNew = int64(20)
)

// vmPlanner plans prodata_vm through the framework.
type vmPlanner struct {
	t      *testing.T
	server tfprotov6.ProviderServer
	typ    tftypes.Object
	// computed names the attributes Terraform fills in from the prior state when the config leaves
	// them null: ProposedNew in Terraform core.
	computed map[string]bool
}

func newVMPlanner(t *testing.T) *vmPlanner {
	t.Helper()
	s := vmSchema(t)
	typ, ok := s.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("the schema type is not an object")
	}
	computed := map[string]bool{}
	for name, attribute := range s.Attributes {
		if attribute.IsComputed() {
			computed[name] = true
		}
	}
	return &vmPlanner{t: t, server: providerserver.NewProtocol6(vmPlanProvider{})(), typ: typ, computed: computed}
}

// object turns attribute values into an object value of the schema: attributes that are not given
// are null.
func (p *vmPlanner) object(v vmValues) tftypes.Value {
	attributes := make(map[string]tftypes.Value, len(p.typ.AttributeTypes))
	for name, typ := range p.typ.AttributeTypes {
		if value, ok := v[name]; ok {
			attributes[name] = value
		} else {
			attributes[name] = tftypes.NewValue(typ, nil)
		}
	}
	return tftypes.NewValue(p.typ, attributes)
}

// proposed is what Terraform core hands to the provider as the proposed new state: the config, with
// the prior value for every computed attribute the config leaves null.
func (p *vmPlanner) proposed(prior, config vmValues) vmValues {
	out := make(vmValues, len(p.typ.AttributeTypes))
	for name, typ := range p.typ.AttributeTypes {
		value, given := config[name]
		if !given {
			value = tftypes.NewValue(typ, nil)
		}
		if p.computed[name] && value.IsNull() {
			if old, ok := prior[name]; ok {
				value = old
			}
		}
		out[name] = value
	}
	return out
}

func (p *vmPlanner) dynamic(v vmValues) *tfprotov6.DynamicValue {
	p.t.Helper()
	dv, err := tfprotov6.NewDynamicValue(p.typ, p.object(v))
	if err != nil {
		p.t.Fatalf("encoding the value: %v", err)
	}
	return &dv
}

// vmPlan is the answer of PlanResourceChange.
type vmPlan struct {
	planned         vmValues
	requiresReplace []string
	errors          []string
	warnings        []string
}

// plan runs PlanResourceChange for an update of prior to config.
func (p *vmPlanner) plan(prior, config vmValues) vmPlan {
	p.t.Helper()
	resp, err := p.server.PlanResourceChange(context.Background(), &tfprotov6.PlanResourceChangeRequest{
		TypeName:         "prodata_vm",
		PriorState:       p.dynamic(prior),
		ProposedNewState: p.dynamic(p.proposed(prior, config)),
		Config:           p.dynamic(config),
	})
	if err != nil {
		p.t.Fatalf("PlanResourceChange: %v", err)
	}
	var out vmPlan
	for _, d := range resp.Diagnostics {
		text := d.Summary + ": " + d.Detail
		switch d.Severity {
		case tfprotov6.DiagnosticSeverityError:
			out.errors = append(out.errors, text)
		case tfprotov6.DiagnosticSeverityWarning:
			out.warnings = append(out.warnings, text)
		}
	}
	if resp.PlannedState == nil {
		return out
	}
	value, err := resp.PlannedState.Unmarshal(p.typ)
	if err != nil {
		p.t.Fatalf("decoding the planned state: %v", err)
	}
	var attributes map[string]tftypes.Value
	if err := value.As(&attributes); err != nil {
		p.t.Fatalf("reading the planned state: %v", err)
	}
	out.planned = attributes

	// Terraform core keeps a path the provider asks to replace only when the planned value of it is
	// not known to equal the prior one (node_resource_abstract_instance.go: `!eqV.IsKnown() ||
	// eqV.False()`). That is what lets an attribute with [RequiresReplace, UseStateForUnknown]
	// stay in place while it is left unset.
	priorObject := p.object(prior)
	for _, path := range resp.RequiresReplace {
		before, _, err := tftypes.WalkAttributePath(priorObject, path)
		if err != nil {
			p.t.Fatalf("walking %s in the prior state: %v", path, err)
		}
		after, _, err := tftypes.WalkAttributePath(value, path)
		if err != nil {
			p.t.Fatalf("walking %s in the planned state: %v", path, err)
		}
		beforeValue, ok := before.(tftypes.Value)
		if !ok {
			p.t.Fatalf("%s in the prior state is a %T, not a value", path, before)
		}
		plannedValue, ok := after.(tftypes.Value)
		if !ok {
			p.t.Fatalf("%s in the planned state is a %T, not a value", path, after)
		}
		if plannedValue.IsFullyKnown() && plannedValue.Equal(beforeValue) {
			continue
		}
		out.requiresReplace = append(out.requiresReplace, attributeNameOf(path))
	}
	sort.Strings(out.requiresReplace)
	return out
}

// attributeNameOf is the name of the top-level attribute a path ends in.
func attributeNameOf(path *tftypes.AttributePath) string {
	if name, ok := path.LastStep().(tftypes.AttributeName); ok {
		return string(name)
	}
	return path.String()
}

// vmState is a running VM in network vmNetOld, as the state of the resource has it.
func vmState() vmValues {
	return vmValues{
		"id":               vmNum(7),
		"guid":             vmStr("guid-7"),
		"region":           vmStr("uz5"),
		"project_tag":      vmStr("project-1"),
		"name":             vmStr("web-1"),
		"image_id":         vmNum(3),
		"image_name":       vmStr("Ubuntu 24.04"),
		"image_slug":       vmStr("ubuntu-24.04"),
		"cpu_cores":        vmNum(2),
		"ram":              vmNum(4),
		"disk_size":        vmNum(20),
		"disk_type":        vmStr("SSD"),
		"local_network_id": vmNum(vmNetOld),
		"private_ip":       vmStr("192.168.0.5"),
		"public_ip_id":     vmNullN,
		"public_ip":        vmNullS,
		"status":           vmStr("RUNNING"),
	}
}

// vmConfig is the configuration that matches vmState: what the user wrote, computed attributes left
// null.
func vmConfig() vmValues {
	return vmValues{
		"name":             vmStr("web-1"),
		"image_id":         vmNum(3),
		"cpu_cores":        vmNum(2),
		"ram":              vmNum(4),
		"disk_size":        vmNum(20),
		"disk_type":        vmStr("SSD"),
		"local_network_id": vmNum(vmNetOld),
	}
}

// vmTimeouts is the timeouts attribute as the config has it.
func vmTimeouts(t *testing.T, p *vmPlanner, create, update tftypes.Value) tftypes.Value {
	t.Helper()
	typ, ok := p.typ.AttributeTypes["timeouts"].(tftypes.Object)
	if !ok {
		t.Fatal("timeouts is not an object")
	}
	return tftypes.NewValue(typ, map[string]tftypes.Value{"create": create, "update": update})
}

const (
	moveSummary    = "Changing local_network_id moves the virtual machine to another network"
	mayMoveSummary = "Changing local_network_id may move the virtual machine to another network"
	unknownMarker  = "?"
	sameNameNotice = "Resource replacement with same name"
)

// TestVmPlan_NetworkAndAddress pins the table of what a plan does to the network and the address of a
// VM, through the framework the way Terraform runs it: whether the VM is replaced, what private_ip
// is planned as, whether status is left for apply to say, and what the user is warned of.
func TestVmPlan_NetworkAndAddress(t *testing.T) {
	p := newVMPlanner(t)
	stopped := vmState().with("status", vmStr("STOPPED"))

	cases := []struct {
		name          string
		state, config vmValues
		replace       []string
		wantIP        string // unknownMarker: left for apply
		wantStatus    string // unknownMarker: left for apply
		warning       string // a warning that has to be there, "" for none of the two below
	}{
		{name: "nothing changes", config: vmConfig(), wantIP: "192.168.0.5", wantStatus: "RUNNING"},
		{name: "only the name changes", config: vmConfig().with("name", vmStr("web-2")), wantIP: "192.168.0.5", wantStatus: "RUNNING"},
		{name: "cpu changes", config: vmConfig().with("cpu_cores", vmNum(4)), wantIP: "192.168.0.5", wantStatus: unknownMarker},
		{name: "ram changes", config: vmConfig().with("ram", vmNum(8)), wantIP: "192.168.0.5", wantStatus: unknownMarker},
		{name: "disk size changes", config: vmConfig().with("disk_size", vmNum(40)), wantIP: "192.168.0.5", wantStatus: unknownMarker},
		{name: "disk type changes", config: vmConfig().with("disk_type", vmStr("NVME")), wantIP: "192.168.0.5", wantStatus: unknownMarker},

		{name: "network changes, no address", config: vmConfig().with("local_network_id", vmNum(vmNetNew)),
			wantIP: unknownMarker, wantStatus: unknownMarker, warning: moveSummary},
		{name: "network changes, address given",
			config: vmConfig().with("local_network_id", vmNum(vmNetNew)).with("private_ip", vmStr("192.168.1.9")),
			wantIP: "192.168.1.9", wantStatus: unknownMarker, warning: moveSummary},
		{name: "network changes, address the same as before",
			config: vmConfig().with("local_network_id", vmNum(vmNetNew)).with("private_ip", vmStr("192.168.0.5")),
			wantIP: "192.168.0.5", wantStatus: unknownMarker, warning: moveSummary},
		{name: "network changes, address unknown",
			config: vmConfig().with("local_network_id", vmNum(vmNetNew)).with("private_ip", vmUnkS),
			wantIP: unknownMarker, wantStatus: unknownMarker, warning: moveSummary},
		{name: "network unknown, no address", config: vmConfig().with("local_network_id", vmUnkN),
			wantIP: unknownMarker, wantStatus: unknownMarker, warning: mayMoveSummary},
		{name: "network unknown, address given",
			config: vmConfig().with("local_network_id", vmUnkN).with("private_ip", vmStr("192.168.1.9")),
			wantIP: "192.168.1.9", wantStatus: unknownMarker, warning: mayMoveSummary},
		{name: "network changes together with cpu",
			config: vmConfig().with("local_network_id", vmNum(vmNetNew)).with("cpu_cores", vmNum(4)),
			wantIP: unknownMarker, wantStatus: unknownMarker, warning: moveSummary},

		{name: "address changes, network stays", config: vmConfig().with("private_ip", vmStr("192.168.0.9")),
			replace: []string{"private_ip"}, wantIP: "192.168.0.9", wantStatus: "RUNNING", warning: sameNameNotice},
		{name: "address unknown, network stays", config: vmConfig().with("private_ip", vmUnkS),
			replace: []string{"private_ip"}, wantIP: unknownMarker, wantStatus: "RUNNING", warning: sameNameNotice},
		{name: "address given and the same as in the state", config: vmConfig().with("private_ip", vmStr("192.168.0.5")),
			wantIP: "192.168.0.5", wantStatus: "RUNNING"},

		{name: "image changes", config: vmConfig().with("image_id", vmNum(4)),
			replace: []string{"image_id"}, wantIP: "192.168.0.5", wantStatus: "RUNNING", warning: sameNameNotice},
		{name: "image and network change: the VM is replaced, not moved",
			config:  vmConfig().with("image_id", vmNum(4)).with("local_network_id", vmNum(vmNetNew)),
			replace: []string{"image_id"}, wantIP: unknownMarker, wantStatus: "RUNNING", warning: sameNameNotice},

		{name: "stopped in the state, nothing changes", state: stopped, config: vmConfig(), wantIP: "192.168.0.5", wantStatus: "STOPPED"},
		{name: "stopped in the state, cpu changes", state: stopped, config: vmConfig().with("cpu_cores", vmNum(4)),
			wantIP: "192.168.0.5", wantStatus: unknownMarker},
		{name: "stopped in the state, network changes", state: stopped, config: vmConfig().with("local_network_id", vmNum(vmNetNew)),
			wantIP: unknownMarker, wantStatus: unknownMarker, warning: moveSummary},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := tc.state
			if state == nil {
				state = vmState()
			}
			got := p.plan(state, tc.config)
			if len(got.errors) > 0 {
				t.Fatalf("unexpected errors: %v", got.errors)
			}
			if strings.Join(got.requiresReplace, ",") != strings.Join(tc.replace, ",") {
				t.Errorf("replace = %v, want %v", got.requiresReplace, tc.replace)
			}
			checkPlannedString(t, "private_ip", got.planned["private_ip"], tc.wantIP)
			checkPlannedString(t, "status", got.planned["status"], tc.wantStatus)

			moveWarned, mayMoveWarned := false, false
			for _, w := range got.warnings {
				switch {
				case strings.HasPrefix(w, moveSummary):
					moveWarned = true
				case strings.HasPrefix(w, mayMoveSummary):
					mayMoveWarned = true
				}
			}
			switch tc.warning {
			case "":
				if len(got.warnings) > 0 {
					t.Errorf("unexpected warnings: %v", got.warnings)
				}
			default:
				if !strings.Contains(strings.Join(got.warnings, "\n"), tc.warning) {
					t.Errorf("warnings %v do not contain %q", got.warnings, tc.warning)
				}
			}
			if moveWarned != (tc.warning == moveSummary) {
				t.Errorf("the warning that the VM moves: got %v among %v", moveWarned, got.warnings)
			}
			// A network that is not known yet may turn out to be the one the VM is in: the warning says "may", and why.
			if mayMoveWarned != (tc.warning == mayMoveSummary) {
				t.Errorf("the warning that the VM may move: got %v among %v", mayMoveWarned, got.warnings)
			}
			if mayMoveWarned && !strings.Contains(strings.Join(got.warnings, "\n"), "known only after other resources are applied. If it is not the network the VM is in now") {
				t.Errorf("warnings %v do not say that the network is not known and may be the VM's own", got.warnings)
			}
		})
	}
}

func checkPlannedString(t *testing.T, name string, got tftypes.Value, want string) {
	t.Helper()
	switch {
	case want == unknownMarker:
		if got.IsKnown() {
			t.Errorf("%s is planned as %v, want it left for apply", name, got)
		}
	case !got.IsKnown():
		t.Errorf("%s is planned as unknown, want %q", name, want)
	default:
		var value string
		if err := got.As(&value); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if value != want {
			t.Errorf("%s is planned as %q, want %q", name, value, want)
		}
	}
}

// TestVmPlan_UpdateTimeoutForAMove: a plan that moves the VM is refused when timeouts.update is set
// below what a move needs, and only then.
func TestVmPlan_UpdateTimeoutForAMove(t *testing.T) {
	p := newVMPlanner(t)
	move := vmConfig().with("local_network_id", vmNum(vmNetNew))
	resize := vmConfig().with("cpu_cores", vmNum(4))
	replace := vmConfig().with("image_id", vmNum(4)).with("local_network_id", vmNum(vmNetNew))

	cases := []struct {
		name    string
		config  vmValues
		timeout tftypes.Value // the timeouts attribute
		refused bool
	}{
		{"no timeouts block", move, tftypes.Value{}, false},
		{"update 30m, the default", move, vmTimeouts(t, p, vmNullS, vmStr("30m")), false},
		{"update 20m, the least", move, vmTimeouts(t, p, vmNullS, vmStr("20m")), false},
		{"update 90m", move, vmTimeouts(t, p, vmNullS, vmStr("90m")), false},
		{"update 19m59s", move, vmTimeouts(t, p, vmNullS, vmStr("19m59s")), true},
		{"update 10m", move, vmTimeouts(t, p, vmNullS, vmStr("10m")), true},
		{"update 10m and create 5m", move, vmTimeouts(t, p, vmStr("5m"), vmStr("10m")), true},
		{"only create is set", move, vmTimeouts(t, p, vmStr("5m"), vmNullS), false},
		{"update is not known yet", move, vmTimeouts(t, p, vmNullS, vmUnkS), false},
		{"update 10m, but no move", resize, vmTimeouts(t, p, vmNullS, vmStr("10m")), false},
		{"update 10m, but the VM is replaced", replace, vmTimeouts(t, p, vmNullS, vmStr("10m")), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := tc.config
			if tc.timeout.Type() != nil {
				config = config.with("timeouts", tc.timeout)
			}
			got := p.plan(vmState(), config)
			gotRefused := false
			for _, e := range got.errors {
				if strings.Contains(e, "timeouts.update is too short") {
					gotRefused = true
				} else {
					t.Errorf("unexpected error: %s", e)
				}
			}
			if gotRefused != tc.refused {
				t.Errorf("refused = %v, want %v (errors %v)", gotRefused, tc.refused, got.errors)
			}
		})
	}
}
