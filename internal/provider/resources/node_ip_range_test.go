package resources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestParseIPv4Range(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr string // substring; empty = valid
	}{
		{"valid", "10.0.0.10-10.0.0.20", ""},
		{"two adjacent addresses", "10.0.0.10-10.0.0.11", ""},
		{"crosses an octet boundary", "10.0.0.250-10.0.1.5", ""},
		{"reversed", "10.0.0.20-10.0.0.10", "reversed"},
		{"reversed across octet", "10.0.1.5-10.0.0.250", "reversed"},
		{"single address", "10.0.0.10-10.0.0.10", "single address"},
		{"octet out of range", "999.1.1.1-0.0.0.0", "not a valid IPv4"},
		{"end octet out of range", "10.0.0.1-10.0.0.256", "not a valid IPv4"},
		{"no dash", "10.0.0.10", "start-end"},
		{"extra dash", "10.0.0.10-10.0.0.20-10.0.0.30", "start-end"},
		{"empty", "", "start-end"},
		{"only dash", "-", "not a valid IPv4"},
		{"empty start", "-10.0.0.20", "not a valid IPv4"},
		{"empty end", "10.0.0.10-", "not a valid IPv4"},
		{"space after dash", "10.0.0.10- 10.0.0.20", "not a valid IPv4"},
		{"space before dash", "10.0.0.10 -10.0.0.20", "not a valid IPv4"},
		{"leading space", " 10.0.0.10-10.0.0.20", "not a valid IPv4"},
		{"IPv6", "fd00::1-fd00::9", "not a valid IPv4"},
		{"IPv4-mapped IPv6", "::ffff:10.0.0.1-::ffff:10.0.0.9", "not a valid IPv4"},
		{"leading zeros", "10.0.0.010-10.0.0.20", "not a valid IPv4"},
		{"too few octets", "10.0.0-10.0.0.20", "not a valid IPv4"},
		{"CIDR instead of range", "10.0.0.0/24-10.0.0.20", "not a valid IPv4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end, err := parseIPv4Range(tt.in)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !start.IsValid() || !end.IsValid() || start.Compare(end) >= 0 {
					t.Errorf("bad result: start=%v end=%v", start, end)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got none (start=%v end=%v)", tt.wantErr, start, end)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestIPv4RangeValidator(t *testing.T) {
	tests := []struct {
		name    string
		in      types.String
		wantErr bool
	}{
		{"valid", types.StringValue("10.0.0.10-10.0.0.20"), false},
		{"reversed", types.StringValue("10.0.0.20-10.0.0.10"), true},
		{"garbage that the old regex accepted", types.StringValue("999.1.1.1-0.0.0.0"), true},
		{"null is skipped", types.StringNull(), false},
		{"unknown is skipped", types.StringUnknown(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp validator.StringResponse
			IPv4Range().ValidateString(context.Background(), validator.StringRequest{ConfigValue: tt.in}, &resp)
			if got := resp.Diagnostics.HasError(); got != tt.wantErr {
				t.Errorf("HasError = %v, want %v (%v)", got, tt.wantErr, resp.Diagnostics)
			}
		})
	}
}

// The node_ip_range attribute must actually carry the validator — a unit test of the
// validator alone would stay green if someone dropped it from the schema.
func TestK8sClusterSchema_NodeIPRangeUsesIPv4Range(t *testing.T) {
	var resp resource.SchemaResponse
	NewK8sClusterResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	attr, ok := resp.Schema.Attributes["node_ip_range"]
	if !ok {
		t.Fatal("node_ip_range missing from schema")
	}
	sa, ok := attr.(interface {
		StringValidators() []validator.String
	})
	if !ok {
		t.Fatalf("node_ip_range is %T, want a string attribute", attr)
	}
	var vresp validator.StringResponse
	for _, v := range sa.StringValidators() {
		v.ValidateString(context.Background(), validator.StringRequest{ConfigValue: types.StringValue("10.0.0.20-10.0.0.10")}, &vresp)
	}
	if !vresp.Diagnostics.HasError() {
		t.Error("a reversed node_ip_range must be rejected by the schema validators")
	}
}

func TestCheckNodeIPRange(t *testing.T) {
	net24 := client.LocalNetwork{ID: 7, CIDR: "10.0.1.0/24", Gateway: "10.0.1.1"}
	tests := []struct {
		name      string
		network   client.LocalNetwork
		rng       string
		wantErrs  []string // substrings, one per expected error, in order
		wantWarns []string
	}{
		{"inside CIDR, gateway outside", net24, "10.0.1.10-10.0.1.20", nil, nil},
		{"gateway just below start is fine", net24, "10.0.1.2-10.0.1.20", nil, nil},
		{"gateway just above end is fine", client.LocalNetwork{CIDR: "10.0.1.0/24", Gateway: "10.0.1.21"}, "10.0.1.10-10.0.1.20", nil, nil},
		{"gateway inside range", net24, "10.0.1.0-10.0.1.20", []string{"gateway 10.0.1.1 lies inside"}, []string{"network address"}},
		{"gateway in the middle", client.LocalNetwork{CIDR: "10.0.1.0/24", Gateway: "10.0.1.15"}, "10.0.1.10-10.0.1.20", []string{"gateway 10.0.1.15 lies inside"}, nil},
		{"gateway equals start", client.LocalNetwork{CIDR: "10.0.1.0/24", Gateway: "10.0.1.10"}, "10.0.1.10-10.0.1.20", []string{"gateway 10.0.1.10 lies inside"}, nil},
		{"gateway equals end", client.LocalNetwork{CIDR: "10.0.1.0/24", Gateway: "10.0.1.20"}, "10.0.1.10-10.0.1.20", []string{"gateway 10.0.1.20 lies inside"}, nil},
		{"start outside CIDR", net24, "10.0.0.250-10.0.1.20", []string{"start 10.0.0.250 lies outside", "gateway 10.0.1.1 lies inside"}, nil},
		{"end outside CIDR", net24, "10.0.1.200-10.0.2.5", []string{"end 10.0.2.5 lies outside"}, nil},
		{"both outside, other network", net24, "10.9.9.10-10.9.9.20", []string{"10.9.9.10-10.9.9.20 does not lie inside"}, nil},
		{"range wider than CIDR", net24, "10.0.0.1-10.0.2.1", []string{"does not lie inside", "gateway 10.0.1.1 lies inside"}, nil},
		{"network address in range", client.LocalNetwork{CIDR: "10.0.1.0/24", Gateway: "10.0.1.254"}, "10.0.1.0-10.0.1.9", nil, []string{"network address 10.0.1.0"}},
		{"broadcast in range", client.LocalNetwork{CIDR: "10.0.1.0/24", Gateway: "10.0.1.1"}, "10.0.1.250-10.0.1.255", nil, []string{"broadcast address 10.0.1.255"}},
		{"broadcast just outside range", net24, "10.0.1.240-10.0.1.254", nil, nil},
		{"/16 inside", client.LocalNetwork{CIDR: "10.5.0.0/16", Gateway: "10.5.0.1"}, "10.5.7.10-10.5.7.20", nil, nil},
		{"/16 broadcast", client.LocalNetwork{CIDR: "10.5.0.0/16", Gateway: "10.5.0.1"}, "10.5.255.250-10.5.255.255", nil, []string{"broadcast address 10.5.255.255"}},
		{"/16 end past CIDR", client.LocalNetwork{CIDR: "10.5.0.0/16", Gateway: "10.5.0.1"}, "10.5.255.250-10.6.0.2", []string{"end 10.6.0.2 lies outside"}, nil},
		{"/27 inside", client.LocalNetwork{CIDR: "10.0.1.64/27", Gateway: "10.0.1.65"}, "10.0.1.70-10.0.1.80", nil, nil},
		{"/27 end past CIDR", client.LocalNetwork{CIDR: "10.0.1.64/27", Gateway: "10.0.1.65"}, "10.0.1.70-10.0.1.100", []string{"end 10.0.1.100 lies outside"}, nil},
		{"/27 broadcast", client.LocalNetwork{CIDR: "10.0.1.64/27", Gateway: "10.0.1.65"}, "10.0.1.90-10.0.1.95", nil, []string{"broadcast address 10.0.1.95"}},
		{"non-canonical CIDR is masked", client.LocalNetwork{CIDR: "10.0.1.77/24", Gateway: "10.0.1.1"}, "10.0.1.10-10.0.1.20", nil, nil},
		{"non-canonical CIDR: network address still found", client.LocalNetwork{CIDR: "10.0.1.77/24", Gateway: "10.0.1.254"}, "10.0.1.0-10.0.1.9", nil, []string{"network address 10.0.1.0 of 10.0.1.0/24"}},
		{"no gateway reported", client.LocalNetwork{CIDR: "10.0.1.0/24"}, "10.0.1.1-10.0.1.20", nil, nil},
		{"unparsable gateway: warn, do not guess", client.LocalNetwork{ID: 9, CIDR: "10.0.1.0/24", Gateway: "n/a"}, "10.0.1.1-10.0.1.20", nil, []string{"could not parse the gateway"}},
		{"/30: network and broadcast both in range", client.LocalNetwork{CIDR: "10.0.1.8/30", Gateway: "10.0.1.9"}, "10.0.1.8-10.0.1.11", []string{"gateway 10.0.1.9 lies inside"}, []string{"network address 10.0.1.8", "broadcast address 10.0.1.11"}},
		{"/30: range of the two hosts, gateway outside", client.LocalNetwork{CIDR: "10.0.1.8/30", Gateway: "10.0.1.9"}, "10.0.1.10-10.0.1.11", nil, []string{"broadcast address 10.0.1.11"}},
		{"/31 has no network/broadcast addresses", client.LocalNetwork{CIDR: "10.0.1.10/31", Gateway: "10.0.1.9"}, "10.0.1.10-10.0.1.11", nil, nil},
		{"unparsable CIDR: warn, do not guess", client.LocalNetwork{ID: 9, CIDR: "garbage", Gateway: "10.0.1.1"}, "10.0.1.1-10.0.1.20", nil, []string{"could not parse the CIDR"}},
		{"IPv6 CIDR: warn, do not guess", client.LocalNetwork{ID: 9, CIDR: "fd00::/64"}, "10.0.1.1-10.0.1.20", nil, []string{"could not parse the CIDR"}},
		{"malformed range is an error", net24, "10.0.1.20-10.0.1.10", []string{"reversed"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs, warns := checkNodeIPRange(tt.network, tt.rng)
			assertContains(t, "errors", errs, tt.wantErrs)
			assertContains(t, "warnings", warns, tt.wantWarns)
		})
	}
}

func assertContains(t *testing.T, kind string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d %q, want %d %q", kind, len(got), got, len(want), want)
	}
	for i := range want {
		if !strings.Contains(got[i], want[i]) {
			t.Errorf("%s[%d] = %q, want it to contain %q", kind, i, got[i], want[i])
		}
	}
}

func mustPrefix(t *testing.T, s string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(s)
	if err != nil {
		t.Fatalf("ParsePrefix(%q): %v", s, err)
	}
	return p
}

func TestBroadcastAddr(t *testing.T) {
	tests := []struct{ cidr, want string }{
		{"10.0.1.0/24", "10.0.1.255"},
		{"10.0.1.64/27", "10.0.1.95"},
		{"10.5.0.0/16", "10.5.255.255"},
		{"10.0.0.0/8", "10.255.255.255"},
		{"10.0.1.8/30", "10.0.1.11"},
		{"10.0.1.4/32", "10.0.1.4"},
	}
	for _, tt := range tests {
		p := mustPrefix(t, tt.cidr)
		if got := broadcastAddr(p).String(); got != tt.want {
			t.Errorf("broadcastAddr(%s) = %s, want %s", tt.cidr, got, tt.want)
		}
	}
}

// ---- ModifyPlan / Create wiring ----------------------------------------------------

const lanJSON = `{"success":true,"data":{"id":7,"name":"k8s","cidr":"10.0.1.0/24","gateway":"10.0.1.1","linked":false}}`

type fakePanel struct {
	mu       sync.Mutex
	netCalls int // GET /local-networks/{id}
	other    []string
	scopes   []string // "region|projectTag" query of each local-network lookup
}

func newFakePanel(t *testing.T, status int, body string) (*client.Client, *fakePanel) {
	t.Helper()
	fp := &fakePanel{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fp.mu.Lock()
		if strings.Contains(r.URL.Path, "/api/v2/local-networks/") {
			fp.netCalls++
			fp.scopes = append(fp.scopes, r.URL.Query().Get("region")+"|"+r.URL.Query().Get("projectTag"))
		} else {
			fp.other = append(fp.other, r.Method+" "+r.URL.Path)
		}
		fp.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(client.Config{
		APIBaseURL: srv.URL, APIKeyID: "k", APISecretKey: "s", Region: "TEST", ProjectTag: "proj",
	})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	return c, fp
}

func (fp *fakePanel) calls() int {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	return fp.netCalls
}

func k8sObjType(t *testing.T) (tftypes.Object, schema.Schema) {
	t.Helper()
	var resp resource.SchemaResponse
	NewK8sClusterResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	obj, ok := resp.Schema.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("schema type is not an object")
	}
	return obj, resp.Schema
}

// raw builds an object value with every attribute null except those in set.
func k8sRaw(t *testing.T, set map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	obj, _ := k8sObjType(t)
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

func nirStr(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }
func nirNum(v int64) tftypes.Value  { return tftypes.NewValue(tftypes.Number, v) }
func nirStrUnknown() tftypes.Value  { return tftypes.NewValue(tftypes.String, tftypes.UnknownValue) }
func nirNumUnknown() tftypes.Value  { return tftypes.NewValue(tftypes.Number, tftypes.UnknownValue) }
func nirStrNull() tftypes.Value     { return tftypes.NewValue(tftypes.String, nil) }

// planCase describes one ModifyPlan call. cfgRange/planRange/stateRange use "" for null,
// "?" for unknown.
type planCase struct {
	create     bool   // true -> no prior state
	cfgRange   string // node_ip_range in configuration
	planRange  string // node_ip_range in the plan
	stateRange string // node_ip_range in state (ignored for create)
	cfgNet     int64  // network_id in configuration; -1 = unknown
	planNet    int64  // network_id in the plan; -1 = unknown
	stateNet   int64  // network_id in state; 0 = null (import adoption)
	region     string // region in plan; "" = null
	projectTag string // project_tag in plan; "" = null
}

func rangeVal(s string) tftypes.Value {
	switch s {
	case "":
		return nirStrNull()
	case "?":
		return nirStrUnknown()
	}
	return nirStr(s)
}

func netVal(n int64) tftypes.Value {
	switch n {
	case 0:
		return tftypes.NewValue(tftypes.Number, nil)
	case -1:
		return nirNumUnknown()
	}
	return nirNum(n)
}

func runModifyPlan(t *testing.T, c *client.Client, pc planCase) diag.Diagnostics {
	t.Helper()
	_, sch := k8sObjType(t)
	r := &K8sClusterResource{c: c}

	cfg := k8sRaw(t, map[string]tftypes.Value{"node_ip_range": rangeVal(pc.cfgRange), "network_id": netVal(pc.cfgNet)})
	planSet := map[string]tftypes.Value{"node_ip_range": rangeVal(pc.planRange), "network_id": netVal(pc.planNet)}
	if pc.region != "" {
		planSet["region"] = nirStr(pc.region)
	}
	if pc.projectTag != "" {
		planSet["project_tag"] = nirStr(pc.projectTag)
	}
	plan := k8sRaw(t, planSet)
	state := tftypes.NewValue(cfg.Type(), nil)
	if !pc.create {
		state = k8sRaw(t, map[string]tftypes.Value{"node_ip_range": rangeVal(pc.stateRange), "network_id": netVal(pc.stateNet)})
	}

	req := resource.ModifyPlanRequest{
		Config: tfsdk.Config{Schema: sch, Raw: cfg},
		Plan:   tfsdk.Plan{Schema: sch, Raw: plan},
		State:  tfsdk.State{Schema: sch, Raw: state},
	}
	resp := resource.ModifyPlanResponse{Plan: req.Plan}
	r.ModifyPlan(context.Background(), req, &resp)
	return resp.Diagnostics
}

func hasDiag(d diag.Diagnostics, sev diag.Severity, substr string) bool {
	for _, x := range d {
		if x.Severity() == sev && strings.Contains(x.Summary()+" "+x.Detail(), substr) {
			return true
		}
	}
	return false
}

func TestModifyPlan_NodeIPRange(t *testing.T) {
	const bad = "10.0.1.0-10.0.1.20"   // contains the gateway 10.0.1.1 (and the network address)
	const good = "10.0.1.10-10.0.1.20" // fine on net 7

	tests := []struct {
		name      string
		panel     func(t *testing.T) (*client.Client, *fakePanel)
		pc        planCase
		wantCalls int
		wantErr   string // substring of an error; "" = no error expected
		wantWarn  string // substring of a warning; "" = no warning expected
	}{
		{
			name: "create with the gateway inside the range: error, exactly one lookup",
			pc:   planCase{create: true, cfgRange: bad, planRange: bad, cfgNet: 7, planNet: 7}, wantCalls: 1,
			wantErr: "gateway 10.0.1.1 lies inside",
		},
		{
			name: "create with a range in another network: error",
			pc:   planCase{create: true, cfgRange: "10.9.9.10-10.9.9.20", planRange: "10.9.9.10-10.9.9.20", cfgNet: 7, planNet: 7}, wantCalls: 1,
			wantErr: "does not lie inside the network CIDR",
		},
		{
			name: "create with a good range: clean",
			pc:   planCase{create: true, cfgRange: good, planRange: good, cfgNet: 7, planNet: 7}, wantCalls: 1,
		},
		{
			name: "create with network_id unknown: no lookup (Create re-checks)",
			pc:   planCase{create: true, cfgRange: bad, planRange: bad, cfgNet: -1, planNet: -1}, wantCalls: 0,
		},
		{
			name: "create with range unknown: no lookup, no warning",
			pc:   planCase{create: true, cfgRange: "?", planRange: "?", cfgNet: 7, planNet: 7}, wantCalls: 0,
		},
		{
			name: "create without a range: warning, no lookup",
			pc:   planCase{create: true, cfgRange: "", planRange: "?", cfgNet: 7, planNet: 7}, wantCalls: 0,
			wantWarn: "node_ip_range is not set",
		},
		{
			name: "INVARIANT: existing cluster, range unchanged (gateway inside): no lookup, no diagnostics",
			pc:   planCase{cfgRange: bad, planRange: bad, stateRange: bad, cfgNet: 7, planNet: 7, stateNet: 7}, wantCalls: 0,
		},
		{
			name: "INVARIANT: existing cluster, range omitted in config (auto-allocated): no lookup, no warning",
			pc:   planCase{cfgRange: "", planRange: bad, stateRange: bad, cfgNet: 7, planNet: 7, stateNet: 7}, wantCalls: 0,
		},
		{
			name: "INVARIANT: after import network_id is adopted (state null): no lookup",
			pc:   planCase{cfgRange: bad, planRange: bad, stateRange: bad, cfgNet: 7, planNet: 7, stateNet: 0}, wantCalls: 0,
		},
		{
			name: "existing cluster, range changed to a bad one: error",
			pc:   planCase{cfgRange: bad, planRange: bad, stateRange: good, cfgNet: 7, planNet: 7, stateNet: 7}, wantCalls: 1,
			wantErr: "gateway 10.0.1.1 lies inside",
		},
		{
			name: "existing cluster, range changed to a good one: clean",
			pc:   planCase{cfgRange: good, planRange: good, stateRange: "10.0.1.30-10.0.1.40", cfgNet: 7, planNet: 7, stateNet: 7}, wantCalls: 1,
		},
		{
			name: "existing cluster moved to another network with a pinned range: checked",
			pc:   planCase{cfgRange: good, planRange: good, stateRange: good, cfgNet: 7, planNet: 7, stateNet: 3}, wantCalls: 1,
		},
		{
			name: "existing cluster, range changed but network_id unknown: no lookup",
			pc:   planCase{cfgRange: bad, planRange: bad, stateRange: good, cfgNet: -1, planNet: -1, stateNet: 7}, wantCalls: 0,
		},
		{
			name: "API down on plan: warning and skip, not an error",
			panel: func(t *testing.T) (*client.Client, *fakePanel) {
				return newFakePanel(t, http.StatusBadRequest, `{"success":false,"errors":[{"code":601,"message":"nope"}]}`)
			},
			pc: planCase{create: true, cfgRange: bad, planRange: bad, cfgNet: 7, planNet: 7}, wantCalls: 1,
			wantWarn: "Unable to check node_ip_range",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mk := tt.panel
			if mk == nil {
				mk = func(t *testing.T) (*client.Client, *fakePanel) { return newFakePanel(t, http.StatusOK, lanJSON) }
			}
			c, fp := mk(t)
			diags := runModifyPlan(t, c, tt.pc)

			if got := fp.calls(); got != tt.wantCalls {
				t.Errorf("GET local-networks calls = %d, want %d", got, tt.wantCalls)
			}
			if len(fp.other) != 0 {
				t.Errorf("unexpected API calls: %v", fp.other)
			}
			if tt.wantErr == "" && diags.HasError() {
				t.Errorf("unexpected error: %v", diags)
			}
			if tt.wantErr != "" && !hasDiag(diags, diag.SeverityError, tt.wantErr) {
				t.Errorf("want an error containing %q, got %v", tt.wantErr, diags)
			}
			if tt.wantWarn != "" && !hasDiag(diags, diag.SeverityWarning, tt.wantWarn) {
				t.Errorf("want a warning containing %q, got %v", tt.wantWarn, diags)
			}
			if tt.wantWarn == "" && tt.wantErr == "" && len(diags) != 0 {
				t.Errorf("want no diagnostics, got %v", diags)
			}
		})
	}
}

func TestModifyPlan_NodeIPRange_NetworkAddressIsOnlyAWarning(t *testing.T) {
	c, fp := newFakePanel(t, http.StatusOK, `{"success":true,"data":{"id":7,"cidr":"10.0.1.0/24","gateway":"10.0.1.254"}}`)
	rng := "10.0.1.0-10.0.1.9"
	diags := runModifyPlan(t, c, planCase{create: true, cfgRange: rng, planRange: rng, cfgNet: 7, planNet: 7})
	if diags.HasError() {
		t.Fatalf("network address in range must not be an error: %v", diags)
	}
	if !hasDiag(diags, diag.SeverityWarning, "network address 10.0.1.0") {
		t.Errorf("want a network-address warning, got %v", diags)
	}
	if fp.calls() != 1 {
		t.Errorf("calls = %d, want 1", fp.calls())
	}
}

func TestModifyPlan_NodeIPRange_NilClientDoesNotPanic(t *testing.T) {
	// The provider may not be configured yet at plan time (r.c == nil).
	diags := runModifyPlan(t, nil, planCase{create: true, cfgRange: "10.0.1.0-10.0.1.20", planRange: "10.0.1.0-10.0.1.20", cfgNet: 7, planNet: 7})
	if diags.HasError() {
		t.Errorf("unexpected error: %v", diags)
	}
}

func TestCreate_NodeIPRangeSafetyNet(t *testing.T) {
	// network_id unknown at plan time -> Create is the only place the range is checked.
	// It must fail on the lookup/check BEFORE any flavor or create call reaches the panel.
	c, fp := newFakePanel(t, http.StatusOK, lanJSON)
	_, sch := k8sObjType(t)
	r := &K8sClusterResource{c: c}

	rng := "10.0.1.0-10.0.1.20"
	plan := k8sRaw(t, map[string]tftypes.Value{
		"name": nirStr("c"), "node_ip_range": nirStr(rng), "network_id": nirNum(7),
	})
	req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: sch, Raw: plan}}
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(plan.Type(), nil)}}
	r.Create(context.Background(), req, &resp)

	if !hasDiag(resp.Diagnostics, diag.SeverityError, "gateway 10.0.1.1 lies inside") {
		t.Fatalf("want a gateway error from Create, got %v", resp.Diagnostics)
	}
	if fp.calls() != 1 {
		t.Errorf("local-network lookups = %d, want 1", fp.calls())
	}
	if len(fp.other) != 0 {
		t.Errorf("Create must stop before other API calls, got %v", fp.other)
	}
}

// A failed lookup in Create must not block anything: the v2 endpoint only sees networks of
// the request's project while cluster-create accepts any network of the organisation, and
// in a replacement Create runs after the old cluster is already gone. The plan warned.
func TestCreate_NodeIPRangeLookupFailureIsNotFatalAndSilent(t *testing.T) {
	c, fp := newFakePanel(t, http.StatusBadRequest, `{"success":false,"errors":[{"code":601,"message":"nope"}]}`)
	_, sch := k8sObjType(t)
	r := &K8sClusterResource{c: c}

	plan := k8sRaw(t, map[string]tftypes.Value{
		"name": nirStr("c"), "node_ip_range": nirStr("10.0.1.10-10.0.1.20"), "network_id": nirNum(7),
	})
	req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: sch, Raw: plan}}
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(plan.Type(), nil)}}
	r.Create(context.Background(), req, &resp)

	if fp.calls() != 1 {
		t.Errorf("local-network lookups = %d, want 1", fp.calls())
	}
	if hasDiag(resp.Diagnostics, diag.SeverityError, "node_ip_range") || hasDiag(resp.Diagnostics, diag.SeverityWarning, "node_ip_range") {
		t.Errorf("a failed lookup must add no node_ip_range diagnostics in Create: %v", resp.Diagnostics)
	}
	if len(fp.other) == 0 {
		t.Error("Create must carry on to the flavor / create calls after a failed lookup")
	}
}

// Create adds findings only for errors; the warnings were already shown at plan time.
func TestCreate_NodeIPRangeAddsNoWarnings(t *testing.T) {
	c, _ := newFakePanel(t, http.StatusOK, `{"success":true,"data":{"id":7,"cidr":"10.0.1.0/24","gateway":"10.0.1.254"}}`)
	_, sch := k8sObjType(t)
	r := &K8sClusterResource{c: c}

	plan := k8sRaw(t, map[string]tftypes.Value{
		"name": nirStr("c"), "node_ip_range": nirStr("10.0.1.0-10.0.1.9"), "network_id": nirNum(7), // network address in range
	})
	req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: sch, Raw: plan}}
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(plan.Type(), nil)}}
	r.Create(context.Background(), req, &resp)

	if hasDiag(resp.Diagnostics, diag.SeverityWarning, "Suspicious node_ip_range") {
		t.Errorf("Create must not repeat plan-time warnings: %v", resp.Diagnostics)
	}
}

// A panel that accepts the connection and never answers must not hang `terraform plan`.
func TestModifyPlan_NodeIPRange_HungPanelTimesOutWithWarning(t *testing.T) {
	old := nodeIPRangePlanLookupTimeout
	nodeIPRangePlanLookupTimeout = 100 * time.Millisecond
	t.Cleanup(func() { nodeIPRangePlanLookupTimeout = old })

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // hang until the client gives up
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(client.Config{APIBaseURL: srv.URL, APIKeyID: "k", APISecretKey: "s", Region: "TEST", ProjectTag: "proj"})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}

	done := make(chan diag.Diagnostics, 1)
	go func() {
		rng := "10.0.1.10-10.0.1.20"
		done <- runModifyPlan(t, c, planCase{create: true, cfgRange: rng, planRange: rng, cfgNet: 7, planNet: 7})
	}()
	select {
	case diags := <-done:
		if diags.HasError() {
			t.Errorf("a hung panel must not be an error: %v", diags)
		}
		if !hasDiag(diags, diag.SeverityWarning, "(skipped)") {
			t.Errorf("want a skipped warning, got %v", diags)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("ModifyPlan did not return: the plan-time lookup has no deadline")
	}
}

// A cancelled plan (Ctrl-C) must not produce a misleading "skipped" warning.
func TestModifyPlan_NodeIPRange_CancelledPlanIsSilent(t *testing.T) {
	c, _ := newFakePanel(t, http.StatusOK, lanJSON)
	_, sch := k8sObjType(t)
	rng := "10.0.1.10-10.0.1.20"
	cfg := k8sRaw(t, map[string]tftypes.Value{"node_ip_range": nirStr(rng), "network_id": nirNum(7)})
	req := resource.ModifyPlanRequest{
		Config: tfsdk.Config{Schema: sch, Raw: cfg},
		Plan:   tfsdk.Plan{Schema: sch, Raw: cfg},
		State:  tfsdk.State{Schema: sch, Raw: tftypes.NewValue(cfg.Type(), nil)},
	}
	resp := resource.ModifyPlanResponse{Plan: req.Plan}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	(&K8sClusterResource{c: c}).ModifyPlan(ctx, req, &resp)
	if len(resp.Diagnostics) != 0 {
		t.Errorf("a cancelled plan must add no diagnostics, got %v", resp.Diagnostics)
	}
}

// An empty `data` must read as "could not parse the CIDR of network <requested id>", not "network 0".
func TestModifyPlan_NodeIPRange_EmptyNetworkNamesRequestedID(t *testing.T) {
	c, _ := newFakePanel(t, http.StatusOK, `{"success":true}`)
	rng := "10.0.1.10-10.0.1.20"
	diags := runModifyPlan(t, c, planCase{create: true, cfgRange: rng, planRange: rng, cfgNet: 7, planNet: 7})
	if !hasDiag(diags, diag.SeverityWarning, "of local network 7") {
		t.Errorf("want a warning naming network 7, got %v", diags)
	}
	if diags.HasError() {
		t.Errorf("unexpected error: %v", diags)
	}
}

// Create with node_ip_range omitted (unknown in the plan — the common case) must not run
// the preflight at all and must carry on to the flavor / create calls.
func TestCreate_OmittedNodeIPRangeSkipsPreflight(t *testing.T) {
	c, fp := newFakePanel(t, http.StatusBadRequest, `{"success":false,"errors":[{"code":601,"message":"nope"}]}`)
	_, sch := k8sObjType(t)
	r := &K8sClusterResource{c: c}
	plan := k8sRaw(t, map[string]tftypes.Value{
		"name": nirStr("c"), "node_ip_range": nirStrUnknown(), "network_id": nirNum(7), "master_flavor_id": nirNum(1),
	})
	req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: sch, Raw: plan}}
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(plan.Type(), nil)}}
	r.Create(context.Background(), req, &resp)

	if fp.calls() != 0 {
		t.Errorf("local-network lookups = %d, want 0", fp.calls())
	}
	if hasDiag(resp.Diagnostics, diag.SeverityError, "node_ip_range") {
		t.Errorf("an omitted node_ip_range must not produce a node_ip_range error: %v", resp.Diagnostics)
	}
	if len(fp.other) == 0 {
		t.Error("Create should have proceeded to the flavor / create calls")
	}
}

// An existing cluster whose node_ip_range is omitted in config (state holds the
// auto-allocated range) and whose network_id changes must not check the OLD range
// against the new network: that would be a false error.
func TestModifyPlan_NodeIPRange_ExistingOmittedRangeNetworkChanged(t *testing.T) {
	const old = "10.0.1.0-10.0.1.20"
	c, fp := newFakePanel(t, http.StatusOK, lanJSON)
	diags := runModifyPlan(t, c, planCase{cfgRange: "", planRange: old, stateRange: old, cfgNet: 7, planNet: 7, stateNet: 3})
	if fp.calls() != 0 || len(diags) != 0 {
		t.Errorf("calls=%d diags=%v, want 0 and none", fp.calls(), diags)
	}
}

// Every plan-time diagnostic must point at node_ip_range, so Terraform shows the
// offending line of the configuration.
func TestModifyPlan_NodeIPRange_DiagnosticsCarryAttributePath(t *testing.T) {
	want := path.Root("node_ip_range")
	check := func(t *testing.T, diags diag.Diagnostics) {
		t.Helper()
		if len(diags) == 0 {
			t.Fatal("no diagnostics")
		}
		for _, d := range diags {
			if dp, ok := d.(diag.DiagnosticWithPath); !ok || !dp.Path().Equal(want) {
				t.Errorf("diagnostic %q has no node_ip_range path", d.Summary())
			}
		}
	}
	t.Run("error", func(t *testing.T) {
		c, _ := newFakePanel(t, http.StatusOK, lanJSON)
		check(t, runModifyPlan(t, c, planCase{create: true, cfgRange: "10.9.9.10-10.9.9.20", planRange: "10.9.9.10-10.9.9.20", cfgNet: 7, planNet: 7}))
	})
	t.Run("lookup-skipped warning", func(t *testing.T) {
		c, _ := newFakePanel(t, http.StatusBadRequest, `{"success":false,"errors":[{"code":601,"message":"nope"}]}`)
		check(t, runModifyPlan(t, c, planCase{create: true, cfgRange: "10.0.1.10-10.0.1.20", planRange: "10.0.1.10-10.0.1.20", cfgNet: 7, planNet: 7}))
	})
	t.Run("not-set warning", func(t *testing.T) {
		c, _ := newFakePanel(t, http.StatusOK, lanJSON)
		check(t, runModifyPlan(t, c, planCase{create: true, cfgRange: "", planRange: "?", cfgNet: 7, planNet: 7}))
	})
	t.Run("suspicious-range warning", func(t *testing.T) {
		c, _ := newFakePanel(t, http.StatusOK, `{"success":true,"data":{"id":7,"cidr":"10.0.1.0/24","gateway":"10.0.1.254"}}`)
		check(t, runModifyPlan(t, c, planCase{create: true, cfgRange: "10.0.1.0-10.0.1.9", planRange: "10.0.1.0-10.0.1.9", cfgNet: 7, planNet: 7}))
	})
}

// The error must tell a user who hits it while destroying with a replacement pending how
// to get out (the plan-time check also runs for the refresh that precedes a destroy).
func TestModifyPlan_NodeIPRange_ErrorCarriesDestroyHint(t *testing.T) {
	c, _ := newFakePanel(t, http.StatusOK, lanJSON)
	bad := "10.0.1.0-10.0.1.20"
	diags := runModifyPlan(t, c, planCase{create: true, cfgRange: bad, planRange: bad, cfgNet: 7, planNet: 7})
	if !hasDiag(diags, diag.SeverityError, "terraform destroy -refresh=false") {
		t.Errorf("error detail must mention the -refresh=false escape hatch: %v", diags)
	}
}
