package resources

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Tests for exclude_credentials_from_state: with it on, kube_config and private_key_encoded
// are null in every plan and every state the resource writes, and nothing else changes.

func credB64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// Distinct markers: if one turns up in a plan or in state, a credential got there.
var (
	credMarkerCA         = credB64("FAKE-CA-PEM")
	credMarkerCert       = credB64("FAKE-CLIENT-CERT-PEM")
	credMarkerKey        = credB64("FAKE-CLIENT-KEY-PEM")
	credMarkerPrivateKey = "FAKE-SSH-PRIVATE-KEY-MARKER"
)

const credPublicKey = "ssh-rsa AAAAFAKEPUBLICKEY node@cluster"

func credMarkers() []string {
	return []string{
		credMarkerCA, credMarkerCert, credMarkerKey, credMarkerPrivateKey,
		"FAKE-CA-PEM", "FAKE-CLIENT-CERT-PEM", "FAKE-CLIENT-KEY-PEM",
	}
}

func credKubeconfigYAML() string {
	return `apiVersion: v1
kind: Config
current-context: admin@tfc
clusters:
- name: tfc
  cluster:
    server: https://k8s.example.com:6443
    certificate-authority-data: ` + credMarkerCA + `
contexts:
- name: admin@tfc
  context:
    cluster: tfc
    user: admin
users:
- name: admin
  user:
    client-certificate-data: ` + credMarkerCert + `
    client-key-data: ` + credMarkerKey + `
`
}

func credBool(v bool) tftypes.Value  { return tftypes.NewValue(tftypes.Bool, v) }
func credBoolNull() tftypes.Value    { return tftypes.NewValue(tftypes.Bool, nil) }
func credBoolUnknown() tftypes.Value { return tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue) }
func credFlag(name string) tftypes.Value {
	switch name {
	case "true":
		return credBool(true)
	case "false":
		return credBool(false)
	case "unknown":
		return credBoolUnknown()
	}
	return credBoolNull()
}

func credKubeConfigType(t *testing.T) tftypes.Object {
	t.Helper()
	obj, _ := k8sObjType(t)
	typ, ok := obj.AttributeTypes["kube_config"].(tftypes.Object)
	if !ok {
		t.Fatal("kube_config is not an object attribute")
	}
	return typ
}

func credKubeConfigUnknown(t *testing.T) tftypes.Value {
	t.Helper()
	return tftypes.NewValue(credKubeConfigType(t), tftypes.UnknownValue)
}

// credKubeConfigKnown is a kube_config object as a previous apply would have stored it.
func credKubeConfigKnown(t *testing.T) tftypes.Value {
	t.Helper()
	typ := credKubeConfigType(t)
	vals := make(map[string]tftypes.Value, len(typ.AttributeTypes))
	for k, at := range typ.AttributeTypes {
		vals[k] = tftypes.NewValue(at, nil)
	}
	vals["host"] = nirStr("https://k8s.example.com:6443")
	vals["client_key"] = nirStr(credMarkerKey)
	return tftypes.NewValue(typ, vals)
}

func assertNoCredMarkers(t *testing.T, what, text string) {
	t.Helper()
	for _, m := range credMarkers() {
		if strings.Contains(text, m) {
			t.Errorf("%s contains %q", what, m)
		}
	}
}

func TestCredentialsExcluded(t *testing.T) {
	tests := []struct {
		name string
		in   types.Bool
		want bool
	}{
		{"null (not set)", types.BoolNull(), false},
		{"unknown", types.BoolUnknown(), false},
		{"false", types.BoolValue(false), false},
		{"true", types.BoolValue(true), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := credentialsExcluded(tt.in); got != tt.want {
				t.Errorf("credentialsExcluded(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// ---- applyServerState ----------------------------------------------------------------

func credCluster() *client.Cluster {
	return &client.Cluster{
		ID: 313, Name: "tfc", Status: client.ClusterStatusSuccess, KubeVersion: "v1.31.4",
		APIEndpoint: "https://k8s.example.com:6443", PodSubnet: "10.244.0.0/16",
		NodeIPRange: "10.0.1.10-10.0.1.20",
		Kubeconfig:  credB64(credKubeconfigYAML()), PrivateKeyEncoded: credMarkerPrivateKey,
		SSHKeyEncoded: credPublicKey,
	}
}

func TestApplyServerState_Credentials(t *testing.T) {
	tests := []struct {
		name        string
		exclude     types.Bool
		wantStored  bool
		wantPrivate string
	}{
		{"flag not set stores credentials", types.BoolNull(), true, credMarkerPrivateKey},
		{"flag false stores credentials", types.BoolValue(false), true, credMarkerPrivateKey},
		{"flag true excludes credentials", types.BoolValue(true), false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := K8sClusterModel{ExcludeCredentialsFromState: tt.exclude}
			(&K8sClusterResource{}).applyServerState(context.Background(), &m, credCluster(), "TEST", "proj")

			if tt.wantStored {
				if m.KubeConfig.IsNull() {
					t.Fatal("kube_config must be populated")
				}
				host, ok := m.KubeConfig.Attributes()["host"].(types.String)
				if !ok || host.ValueString() != "https://k8s.example.com:6443" {
					t.Errorf("kube_config.host = %v", m.KubeConfig.Attributes()["host"])
				}
				if m.PrivateKeyEncoded.ValueString() != tt.wantPrivate {
					t.Errorf("private_key_encoded = %v, want %q", m.PrivateKeyEncoded, tt.wantPrivate)
				}
			} else {
				if !m.KubeConfig.IsNull() || !m.PrivateKeyEncoded.IsNull() {
					t.Errorf("kube_config / private_key_encoded must be null: %v / %v", m.KubeConfig, m.PrivateKeyEncoded)
				}
				// A null object must still carry the schema's type, or State.Set rejects it.
				if len(m.KubeConfig.AttributeTypes(context.Background())) != len(kubeConfigAttrTypes()) {
					t.Errorf("null kube_config lost its attribute types: %v", m.KubeConfig.AttributeTypes(context.Background()))
				}
			}

			// Whatever the flag says, everything else is still written, including the
			// public half of the SSH key.
			if m.SSHKeyEncoded.ValueString() != credPublicKey {
				t.Errorf("ssh_key_encoded = %v, want the public key regardless of the flag", m.SSHKeyEncoded)
			}
			if m.Status.ValueString() != client.ClusterStatusSuccess || m.APIEndpoint.ValueString() != "https://k8s.example.com:6443" ||
				m.KubernetesVersion.ValueString() != "v1.31.4" || m.ID.ValueInt64() != 313 {
				t.Errorf("non-credential fields must be unaffected: %+v", m)
			}
		})
	}
}

// ---- ModifyPlan ----------------------------------------------------------------------

type credPlanned struct {
	kubeConfig  types.Object
	privateKey  types.String
	apiEndpoint types.String
	rawPlan     string
	diags       diag.Diagnostics
}

// runCredentialsModifyPlan hands ModifyPlan a plan shaped the way the framework would: the
// server-owned attributes unknown (they have no UseStateForUnknown), the flag as configured.
// Config and plan are built separately — the flag is in both, the unknowns only in the plan.
func runCredentialsModifyPlan(t *testing.T, create bool, flag string, versionChanged bool) credPlanned {
	t.Helper()
	return runCredentialsModifyPlanWith(t, create, flag, versionChanged, nil)
}

// runCredentialsModifyPlanWith is runCredentialsModifyPlan with more of the configuration
// set: extra goes into the configuration, the plan and, for an existing cluster, the state.
func runCredentialsModifyPlanWith(t *testing.T, create bool, flag string, versionChanged bool, extra map[string]tftypes.Value) credPlanned {
	t.Helper()
	_, sch := k8sObjType(t)

	with := func(base map[string]tftypes.Value) map[string]tftypes.Value {
		out := make(map[string]tftypes.Value, len(base)+len(extra))
		for k, v := range base {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	cfg := k8sRaw(t, with(map[string]tftypes.Value{"exclude_credentials_from_state": credFlag(flag)}))

	newVersion := "v1.31.4"
	if versionChanged {
		newVersion = "v1.32.0"
	}
	plan := k8sRaw(t, with(map[string]tftypes.Value{
		"exclude_credentials_from_state": credFlag(flag),
		"kubernetes_version":             nirStr(newVersion),
		"kube_config":                    credKubeConfigUnknown(t),
		"private_key_encoded":            nirStrUnknown(),
		"api_endpoint":                   nirStrUnknown(),
	}))

	state := tftypes.NewValue(cfg.Type(), nil)
	if !create {
		state = k8sRaw(t, with(map[string]tftypes.Value{
			"id":                  nirNum(313),
			"kubernetes_version":  nirStr("v1.31.4"),
			"kube_config":         credKubeConfigKnown(t),
			"private_key_encoded": nirStr(credMarkerPrivateKey),
			"api_endpoint":        nirStr("https://k8s.example.com:6443"),
		}))
	}

	req := resource.ModifyPlanRequest{
		Config: tfsdk.Config{Schema: sch, Raw: cfg},
		Plan:   tfsdk.Plan{Schema: sch, Raw: plan},
		State:  tfsdk.State{Schema: sch, Raw: state},
	}
	resp := resource.ModifyPlanResponse{Plan: req.Plan}
	(&K8sClusterResource{}).ModifyPlan(context.Background(), req, &resp)
	for _, d := range resp.Diagnostics {
		if d.Severity() == diag.SeverityError {
			t.Fatalf("ModifyPlan error: %v", resp.Diagnostics)
		}
	}

	var out credPlanned
	ctx := context.Background()
	if d := resp.Plan.GetAttribute(ctx, path.Root("kube_config"), &out.kubeConfig); d.HasError() {
		t.Fatalf("read planned kube_config: %v", d)
	}
	if d := resp.Plan.GetAttribute(ctx, path.Root("private_key_encoded"), &out.privateKey); d.HasError() {
		t.Fatalf("read planned private_key_encoded: %v", d)
	}
	if d := resp.Plan.GetAttribute(ctx, path.Root("api_endpoint"), &out.apiEndpoint); d.HasError() {
		t.Fatalf("read planned api_endpoint: %v", d)
	}
	out.rawPlan = resp.Plan.Raw.String()
	out.diags = resp.Diagnostics
	return out
}

func TestModifyPlan_ExcludeCredentialsFromState(t *testing.T) {
	tests := []struct {
		name           string
		create         bool
		flag           string
		versionChanged bool
		wantNull       bool // credentials planned as a known null; otherwise left unknown
	}{
		{"create, flag on", true, "true", false, true},
		{"create, flag off", true, "false", false, false},
		{"create, flag not set", true, "null", false, false},
		{"create, flag unknown", true, "unknown", false, false},
		{"update, flag on", false, "true", false, true},
		{"update, flag on, version upgrade", false, "true", true, true},
		{"update, flag off, version upgrade", false, "false", true, false},
		{"update, flag not set, version upgrade", false, "null", true, false},
		{"update, flag unknown", false, "unknown", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runCredentialsModifyPlan(t, tt.create, tt.flag, tt.versionChanged)

			if tt.wantNull {
				if !got.kubeConfig.IsNull() || !got.privateKey.IsNull() {
					t.Errorf("credentials must be planned as null: kube_config=%v private_key_encoded=%v",
						got.kubeConfig, got.privateKey)
				}
				assertNoCredMarkers(t, "plan", got.rawPlan)
			} else if !got.kubeConfig.IsUnknown() || !got.privateKey.IsUnknown() {
				// Without the exclusion ModifyPlan must leave the framework's unknowns alone,
				// or a stored credential would be planned as a value it cannot yet know.
				t.Errorf("credentials must stay unknown: kube_config=%v private_key_encoded=%v",
					got.kubeConfig, got.privateKey)
			}

			// The exclusion is about the credentials only. An in-place version upgrade still
			// rewrites api_endpoint server-side (ADR-K3), so it stays unknown in the plan; on
			// create the framework's own unknown is left as it was.
			if (tt.versionChanged || tt.create) && !got.apiEndpoint.IsUnknown() {
				t.Errorf("api_endpoint must stay unknown, got %v", got.apiEndpoint)
			}
		})
	}
}

// ---- the generated-SSH-key warning ---------------------------------------------------------

const sshKeyWarning = "SSH private key will not be kept"

// sshWarnings are the diagnostics of a ModifyPlan run that are the generated-key warning.
func sshWarnings(diags diag.Diagnostics) diag.Diagnostics {
	var out diag.Diagnostics
	for _, d := range diags {
		if d.Severity() == diag.SeverityWarning && strings.Contains(d.Summary(), sshKeyWarning) {
			out = append(out, d)
		}
	}
	return out
}

// The warning is for a cluster that is being created. Its advice, "set public_key", can
// only work then: the SSH key is fixed when the cluster is created, and the provider takes
// a public_key added later without sending it anywhere. On an existing cluster the same
// advice would silently do nothing, so nothing is said there.
func TestModifyPlan_GeneratedSSHKeyNotKeptWarning(t *testing.T) {
	const publicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFAKEKEY user@host"

	tests := []struct {
		name     string
		create   bool
		flag     string
		ssh      tftypes.Value
		key      tftypes.Value
		wantWarn bool
	}{
		// Creating a cluster: the platform generates the key pair unless it is given a key
		// of more than 5 bytes.
		{"create, generated key", true, "true", credBool(true), nirStrNull(), true},
		{"create, empty key", true, "true", credBool(true), nirStr(""), true},
		{"create, blank key", true, "true", credBool(true), nirStr("  "), true},
		{"create, placeholder key (4 bytes)", true, "true", credBool(true), nirStr("none"), true},
		{"create, 5-byte key is still not taken", true, "true", credBool(true), nirStr("12345"), true},
		{"create, 6-byte key is taken as given", true, "true", credBool(true), nirStr("123456"), false},
		// The platform counts bytes of the value as sent; it does not trim.
		{"create, 7 blanks are taken as given", true, "true", credBool(true), nirStr("       "), false},
		{"create, own key", true, "true", credBool(true), nirStr(publicKey), false},
		{"create, key not known yet", true, "true", credBool(true), nirStrUnknown(), false},
		{"create, ssh off", true, "true", credBool(false), nirStrNull(), false},
		{"create, ssh not set", true, "true", credBoolNull(), nirStrNull(), false},
		{"create, ssh not known yet", true, "true", credBoolUnknown(), nirStrNull(), false},
		{"create, flag off", true, "false", credBool(true), nirStrNull(), false},
		{"create, flag not set", true, "null", credBool(true), nirStrNull(), false},
		{"create, flag not known yet", true, "unknown", credBool(true), nirStrNull(), false},
		// An existing cluster already has its key pair; turning the flag on there is the
		// documented way to drop the private half from state, and the advice would not work.
		{"existing cluster, generated key", false, "true", credBool(true), nirStrNull(), false},
		{"existing cluster, key set later", false, "true", credBool(true), nirStr(publicKey), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runCredentialsModifyPlanWith(t, tt.create, tt.flag, false, map[string]tftypes.Value{
				"ssh_access_enabled": tt.ssh,
				"public_key":         tt.key,
			})
			warns := sshWarnings(got.diags)
			if !tt.wantWarn {
				if len(warns) != 0 {
					t.Fatalf("want no warning about the SSH private key, got %v", warns)
				}
				return
			}
			if len(warns) != 1 {
				t.Fatalf("want exactly one warning about the SSH private key, got %v", got.diags)
			}
			dp, ok := warns[0].(diag.DiagnosticWithPath)
			if !ok || !dp.Path().Equal(path.Root("exclude_credentials_from_state")) {
				t.Errorf("warning is not attached to exclude_credentials_from_state: %#v", warns[0])
			}
		})
	}
}

// The warning does not depend on the control-plane sizing being settled: size and flavor
// are often unknown while the rest of the configuration is already complete.
func TestModifyPlan_GeneratedSSHKeyWarningIgnoresUnknownSizing(t *testing.T) {
	got := runCredentialsModifyPlanWith(t, true, "true", false, map[string]tftypes.Value{
		"ssh_access_enabled": credBool(true),
		"control_plane_size": nirStrUnknown(),
		"master_flavor_id":   nirNumUnknown(),
	})
	if len(sshWarnings(got.diags)) != 1 {
		t.Fatalf("want the warning with unknown sizing, got %v", got.diags)
	}
}

// ValidateConfig has no state to tell a new cluster from an existing one, so it must stay
// silent: the same configuration is warned about at plan time, for a create only.
func TestValidateConfig_NoGeneratedSSHKeyWarning(t *testing.T) {
	diags := runClusterValidateConfig(t, map[string]tftypes.Value{
		"control_plane_size":             nirStr("small"),
		"exclude_credentials_from_state": credBool(true),
		"ssh_access_enabled":             credBool(true),
	})
	if len(diags) != 0 {
		t.Fatalf("want no diagnostics, got %v", diags)
	}
}

// ---- Create's "kubeconfig not available yet" warning -------------------------------------

func TestKubeconfigPendingDiag(t *testing.T) {
	const wantSummary = "Cluster is ready but its kubeconfig is not yet available"

	tests := []struct {
		name      string
		exclude   types.Bool
		wantIn    []string
		wantNotIn []string
	}{
		{"flag not set", types.BoolNull(), []string{"`terraform refresh`"}, []string{"ephemeral"}},
		{"flag off", types.BoolValue(false), []string{"`terraform refresh`"}, []string{"ephemeral"}},
		{"flag not known", types.BoolUnknown(), []string{"`terraform refresh`"}, []string{"ephemeral"}},
		// Nothing is stored, so a refresh would achieve nothing: the text must point at the
		// ephemeral resource instead.
		{"flag on", types.BoolValue(true), []string{"prodata_kubernetes_kubeconfig", "nothing to refresh"}, []string{"`terraform refresh`"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary, detail := kubeconfigPendingDiag(313, tt.exclude)
			if summary != wantSummary {
				t.Errorf("summary = %q, want %q", summary, wantSummary)
			}
			if !strings.Contains(detail, "Cluster 313 reached SUCCESS") {
				t.Errorf("detail does not name the cluster: %q", detail)
			}
			for _, s := range tt.wantIn {
				if !strings.Contains(detail, s) {
					t.Errorf("detail %q does not mention %q", detail, s)
				}
			}
			for _, s := range tt.wantNotIn {
				if strings.Contains(detail, s) {
					t.Errorf("detail %q must not mention %q", detail, s)
				}
			}
		})
	}
}

// ---- Create / Read / Update against a fake panel ---------------------------------------

// credClusterView is what the fake panel's getCluster reports about the cluster.
type credClusterView struct {
	status, version, secret, private string
	blocked                          bool
}

// credPanel is a minimal panel-main: one cluster, id 313, whose getCluster reply carries a
// kubeconfig and an SSH private key. Any request it does not know is a test failure.
type credPanel struct {
	mu       sync.Mutex
	cluster  credClusterView
	requests []string

	// failReadsFrom, when set, makes the n-th and every later getCluster call (counting
	// from 1) fail with an HTTP 500; onReadFail, if set, runs as each of them is served.
	failReadsFrom int
	onReadFail    func()
	reads         int

	// onRead, if set, runs as each getCluster call is served, with its number counting from 1.
	onRead func(n int)
}

func newCredPanel(t *testing.T) (*client.Client, *credPanel) {
	t.Helper()
	p := &credPanel{cluster: credClusterView{
		status: client.ClusterStatusSuccess, version: "v1.31.4",
		secret: credB64(credKubeconfigYAML()), private: credMarkerPrivateKey,
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const base = "/panel-main/api/kubernetes"

		p.mu.Lock()
		p.requests = append(p.requests, r.Method+" "+r.URL.Path)
		failRead, onReadFail, onRead, read := false, p.onReadFail, p.onRead, 0
		if r.Method == http.MethodGet && r.URL.Path == base+"/getCluster/313" {
			p.reads++
			read = p.reads
			failRead = p.failReadsFrom > 0 && p.reads >= p.failReadsFrom
		}
		if r.Method == http.MethodPost && r.URL.Path == base+"/updateClusterKuberVersion/313" {
			p.cluster.version = r.URL.Query().Get("version")
		}
		view := p.cluster
		p.mu.Unlock()

		var data any
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, base+"/getMasterNodeConfig/"):
			data = []map[string]any{{"id": 7, "cpu": 2, "ram": 4, "ssd": 20, "isHa": false}}
		case r.Method == http.MethodGet && r.URL.Path == base+"/getClusters":
			data = []any{}
		case r.Method == http.MethodPost && r.URL.Path == base+"/createCluster":
			data = credClusterBody(credClusterView{status: "NEW", version: view.version})
		case r.Method == http.MethodPost && r.URL.Path == base+"/updateClusterKuberVersion/313":
			data = credClusterBody(view)
		case r.Method == http.MethodGet && r.URL.Path == base+"/getCluster/313":
			if onRead != nil {
				onRead(read)
			}
			if failRead {
				if onReadFail != nil {
					onReadFail()
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":500,"errMessage":"boom","data":null}`))
				return
			}
			data = credClusterBody(view)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "errMessage": nil, "data": data})
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(client.Config{APIBaseURL: srv.URL, APIKeyID: "k", APISecretKey: "s", Region: "TEST", ProjectTag: "proj"})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	return c, p
}

// setCluster changes how the cluster looks to the panel's next replies.
func (p *credPanel) setCluster(status string, blocked bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cluster.status, p.cluster.blocked = status, blocked
}

// failReads makes the n-th and every later getCluster call fail (see credPanel.failReadsFrom).
func (p *credPanel) failReads(from int, onFail func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failReadsFrom, p.onReadFail = from, onFail
}

// watchReads makes fn run as each getCluster call is served, with the call's number counting
// from 1. It runs on the fake panel's side of the connection, while the caller waits for the reply.
func (p *credPanel) watchReads(fn func(n int)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onRead = fn
}

// requestsSeen returns "METHOD path" for every request so far.
func (p *credPanel) requestsSeen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.requests...)
}

func credClusterBody(v credClusterView) map[string]any {
	return map[string]any{
		"id": 313, "name": "tfc", "status": map[string]any{"name": v.status},
		"kuberVersion": v.version, "apiEndpoint": "https://k8s.example.com:6443",
		"isPublic": false, "isHa": false, "blocked": v.blocked,
		"sshKeyEncoded": credPublicKey, "privateKeyEncoded": v.private, "clusterConfigSecret": v.secret,
		"masterNodeCount": 1, "workerNodeCount": 0, "nodePoolCount": 0,
		"masterNodeConfiguration": map[string]any{"id": 7, "cpu": 2, "ram": 4, "ssd": 20, "isHa": false},
		"podSubnet":               "10.244.0.0/16", "nodeIpRange": "10.0.1.10-10.0.1.20",
		"ipAddressesCount": 5, "projectId": 89, "dateCreated": "2026-10-01T00:00:00Z",
	}
}

func credStateModel(t *testing.T, st tfsdk.State) K8sClusterModel {
	t.Helper()
	var m K8sClusterModel
	if d := st.Get(context.Background(), &m); d.HasError() {
		t.Fatalf("read state: %v", d)
	}
	return m
}

// assertCredentialsInState checks both ways a state can look: with the credentials, or — when
// the flag is on — without them anywhere in it.
func assertCredentialsInState(t *testing.T, st tfsdk.State, excluded bool) {
	t.Helper()
	m := credStateModel(t, st)
	if excluded {
		if !m.KubeConfig.IsNull() || !m.PrivateKeyEncoded.IsNull() {
			t.Errorf("kube_config / private_key_encoded must be null: %v / %v", m.KubeConfig, m.PrivateKeyEncoded)
		}
		assertNoCredMarkers(t, "state", st.Raw.String())
	} else {
		if m.KubeConfig.IsNull() || m.PrivateKeyEncoded.ValueString() != credMarkerPrivateKey {
			t.Errorf("kube_config / private_key_encoded must be stored: %v / %v", m.KubeConfig, m.PrivateKeyEncoded)
		}
	}
	if m.SSHKeyEncoded.ValueString() != credPublicKey || m.Status.ValueString() != client.ClusterStatusSuccess ||
		m.APIEndpoint.ValueString() != "https://k8s.example.com:6443" {
		t.Errorf("the other server-owned fields must be written either way: %+v", m)
	}
}

func TestCreate_ExcludeCredentialsFromState(t *testing.T) {
	for _, flag := range []string{"null", "false", "true"} {
		t.Run("flag "+flag, func(t *testing.T) {
			c, panel := newCredPanel(t)
			_, sch := k8sObjType(t)

			plan := k8sRaw(t, map[string]tftypes.Value{
				"name": nirStr("tfc"), "kubernetes_version": nirStr("v1.31.4"),
				"high_availability": credBool(false), "network_id": nirNum(7),
				"pod_cidr": nirStr("10.244.0.0/16"), "master_flavor_id": nirNum(7),
				"ssh_access_enabled": credBool(false), "public_endpoint_enabled": credBool(false),
				"exclude_credentials_from_state": credFlag(flag),
			})
			req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: sch, Raw: plan}}
			resp := resource.CreateResponse{State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(plan.Type(), nil)}}
			(&K8sClusterResource{c: c}).Create(context.Background(), req, &resp)

			if resp.Diagnostics.HasError() || len(resp.Diagnostics) != 0 {
				t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
			}
			assertCredentialsInState(t, resp.State, flag == "true")
			panel.mu.Lock()
			defer panel.mu.Unlock()
			if !containsString(panel.requests, "POST /panel-main/api/kubernetes/createCluster") {
				t.Errorf("the cluster was never created: %v", panel.requests)
			}
		})
	}
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func credStateAndPlan(t *testing.T, stateFlag, planFlag string, stored bool) (tfsdk.State, tfsdk.Plan) {
	t.Helper()
	_, sch := k8sObjType(t)
	common := func(flag string) map[string]tftypes.Value {
		return map[string]tftypes.Value{
			"id": nirNum(313), "region": nirStr("TEST"), "project_tag": nirStr("proj"),
			"name": nirStr("tfc"), "kubernetes_version": nirStr("v1.31.4"),
			"exclude_credentials_from_state": credFlag(flag),
		}
	}
	stateSet := common(stateFlag)
	if stored {
		stateSet["kube_config"] = credKubeConfigKnown(t)
		stateSet["private_key_encoded"] = nirStr(credMarkerPrivateKey)
	}
	planSet := common(planFlag)
	return tfsdk.State{Schema: sch, Raw: k8sRaw(t, stateSet)}, tfsdk.Plan{Schema: sch, Raw: k8sRaw(t, planSet)}
}

func TestRead_ExcludeCredentialsFromState(t *testing.T) {
	for _, flag := range []string{"null", "false", "true"} {
		t.Run("flag "+flag, func(t *testing.T) {
			c, _ := newCredPanel(t)
			st, _ := credStateAndPlan(t, flag, flag, flag != "true")

			resp := resource.ReadResponse{State: st}
			(&K8sClusterResource{c: c}).Read(context.Background(), resource.ReadRequest{State: st}, &resp)

			if resp.Diagnostics.HasError() {
				t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
			}
			assertCredentialsInState(t, resp.State, flag == "true")
		})
	}
}

// Turning the flag on or off is an in-place update that rewrites the credentials in state:
// the values leave state when it goes on and come back when it goes off.
func TestUpdate_ExcludeCredentialsFlagFlips(t *testing.T) {
	tests := []struct {
		name      string
		stateFlag string
		planFlag  string
		stored    bool // were the credentials in the prior state
		excluded  bool // must the new state be free of them
	}{
		{"turned on", "null", "true", true, true},
		{"turned on from false", "false", "true", true, true},
		{"turned off", "true", "false", false, false},
		{"turned off to unset", "true", "null", false, false},
		{"stays on", "true", "true", false, true},
		{"stays off", "false", "false", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newCredPanel(t)
			st, plan := credStateAndPlan(t, tt.stateFlag, tt.planFlag, tt.stored)

			resp := resource.UpdateResponse{State: st}
			(&K8sClusterResource{c: c}).Update(context.Background(), resource.UpdateRequest{State: st, Plan: plan}, &resp)

			if resp.Diagnostics.HasError() {
				t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
			}
			assertCredentialsInState(t, resp.State, tt.excluded)
		})
	}
}

const (
	credGetCluster     = "GET /panel-main/api/kubernetes/getCluster/313"
	credUpgradeCluster = "POST /panel-main/api/kubernetes/updateClusterKuberVersion/313"
)

// credPlanWithVersion returns plan with kubernetes_version set to version.
func credPlanWithVersion(t *testing.T, plan tfsdk.Plan, version string) tfsdk.Plan {
	t.Helper()
	if d := plan.SetAttribute(context.Background(), path.Root("kubernetes_version"), types.StringValue(version)); d.HasError() {
		t.Fatalf("set the planned version: %v", d)
	}
	return plan
}

// Turning the flag on changes nothing on the cluster: it only rewrites state. It is also the
// way credentials get out of state, so a cluster that cannot be modified — failed, or busy
// with an operation — is no reason to refuse it, or to wait for it.
func TestUpdate_FlagOnlyChangeIgnoresTheClustersCondition(t *testing.T) {
	tests := []struct {
		name    string
		status  string
		blocked bool
	}{
		{"failed cluster", client.ClusterStatusFail, false},
		{"busy cluster", client.ClusterStatusSuccess, true},
		{"failed and busy cluster", client.ClusterStatusFail, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, panel := newCredPanel(t)
			panel.setCluster(tt.status, tt.blocked)
			st, plan := credStateAndPlan(t, "null", "true", true)

			// Waiting out a busy cluster takes minutes; the deadline turns that into a failure
			// instead of a slow test.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			resp := resource.UpdateResponse{State: st}
			(&K8sClusterResource{c: c}).Update(ctx, resource.UpdateRequest{State: st, Plan: plan}, &resp)

			if resp.Diagnostics.HasError() {
				t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
			}
			m := credStateModel(t, resp.State)
			if !m.KubeConfig.IsNull() || !m.PrivateKeyEncoded.IsNull() {
				t.Errorf("kube_config / private_key_encoded must be null: %v / %v", m.KubeConfig, m.PrivateKeyEncoded)
			}
			assertNoCredMarkers(t, "state", resp.State.Raw.String())
			if m.Status.ValueString() != tt.status || m.Blocked.ValueBool() != tt.blocked {
				t.Errorf("the cluster's condition must be written as the panel reports it: status=%v blocked=%v", m.Status, m.Blocked)
			}
			if got := panel.requestsSeen(); !slices.Equal(got, []string{credGetCluster}) {
				t.Errorf("requests = %v, want just the one read", got)
			}
		})
	}
}

// A version upgrade does change the cluster, so it still needs one that can be modified —
// and asks for nothing from the panel before knowing that.
func TestUpdate_VersionChangeStillNeedsAModifiableCluster(t *testing.T) {
	c, panel := newCredPanel(t)
	panel.setCluster(client.ClusterStatusFail, false)
	st, plan := credStateAndPlan(t, "null", "null", true)
	plan = credPlanWithVersion(t, plan, "v1.32.0")

	resp := resource.UpdateResponse{State: st}
	(&K8sClusterResource{c: c}).Update(context.Background(), resource.UpdateRequest{State: st, Plan: plan}, &resp)

	errs := resp.Diagnostics.Errors()
	if len(errs) != 1 || errs[0].Summary() != "Cluster is not in a modifiable state" {
		t.Fatalf("want one 'Cluster is not in a modifiable state' error, got %v", resp.Diagnostics)
	}
	if got := panel.requestsSeen(); !slices.Equal(got, []string{credGetCluster}) {
		t.Errorf("requests = %v, want only the check that refused the upgrade", got)
	}
	if m := credStateModel(t, resp.State); m.KubeConfig.IsNull() || m.PrivateKeyEncoded.IsNull() {
		t.Error("the prior state must be left as it was when the update is refused")
	}
}

// The whole upgrade path with the flag on: the cluster is checked, upgraded, waited for and
// read back, and neither the upgrade's new credentials nor the old ones reach state.
func TestUpdate_VersionUpgradeWithCredentialsExcluded(t *testing.T) {
	c, panel := newCredPanel(t)
	st, plan := credStateAndPlan(t, "true", "true", false)
	plan = credPlanWithVersion(t, plan, "v1.32.0")

	resp := resource.UpdateResponse{State: st}
	(&K8sClusterResource{c: c}).Update(context.Background(), resource.UpdateRequest{State: st, Plan: plan}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	m := credStateModel(t, resp.State)
	if m.KubernetesVersion.ValueString() != "v1.32.0" || m.Status.ValueString() != client.ClusterStatusSuccess {
		t.Errorf("version / status = %v / %v, want v1.32.0 / SUCCESS", m.KubernetesVersion, m.Status)
	}
	if !m.KubeConfig.IsNull() || !m.PrivateKeyEncoded.IsNull() {
		t.Errorf("kube_config / private_key_encoded must be null: %v / %v", m.KubeConfig, m.PrivateKeyEncoded)
	}
	assertNoCredMarkers(t, "state", resp.State.Raw.String())
	want := []string{credGetCluster, credUpgradeCluster, credGetCluster, credGetCluster}
	if got := panel.requestsSeen(); !slices.Equal(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
}

// When the read-back after the update fails, the error says what did and did not happen on
// the cluster, and the prior state is kept rather than a half-filled one written.
func TestUpdate_ReadBackFailureSaysWhatHappened(t *testing.T) {
	tests := []struct {
		name       string
		newVersion string // "" = a change of the flag only
		failFrom   int    // which getCluster call is the read-back
		wantIn     string
		wantNotIn  string
	}{
		{"flag change", "", 1, "Nothing was changed on the cluster", "version upgrade"},
		// The reads before it are the upgrade's own: the check and the wait.
		{"version upgrade", "v1.32.0", 3, "The version upgrade itself succeeded", "Nothing was changed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, panel := newCredPanel(t)
			st, plan := credStateAndPlan(t, "null", "true", true)
			if tt.newVersion != "" {
				plan = credPlanWithVersion(t, plan, tt.newVersion)
			}

			// Cancelling as the read fails ends the retry's wait at once; the error text is what
			// is under test, not the retry.
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			panel.failReads(tt.failFrom, cancel)

			resp := resource.UpdateResponse{State: st}
			(&K8sClusterResource{c: c}).Update(ctx, resource.UpdateRequest{State: st, Plan: plan}, &resp)

			errs := resp.Diagnostics.Errors()
			if len(errs) != 1 || errs[0].Summary() != "Unable to read the cluster back after the update" {
				t.Fatalf("want one 'Unable to read the cluster back after the update' error, got %v", resp.Diagnostics)
			}
			if detail := errs[0].Detail(); !strings.Contains(detail, tt.wantIn) || strings.Contains(detail, tt.wantNotIn) {
				t.Errorf("detail %q: want it to say %q and not %q", detail, tt.wantIn, tt.wantNotIn)
			}
			if m := credStateModel(t, resp.State); m.KubeConfig.IsNull() || m.PrivateKeyEncoded.IsNull() {
				t.Error("the prior state must be kept when the read-back fails")
			}
		})
	}
}

// ---- The per-cluster lock (ADR-K7) -----------------------------------------------------
//
// An upgrade changes the cluster, so it queues behind every other operation that holds it —
// a node pool created in the same run, say — and keeps the lock until the cluster has been
// read back. A change that only rewrites state (the flag, the timeouts) is not such an
// operation and must never wait for the lock.

// holdClusterLock takes the per-cluster lock of id, as another operation on that cluster
// would, until release is called or the test ends.
func holdClusterLock(t *testing.T, id int64) (release func()) {
	t.Helper()
	unlock := lockCluster(id)
	var once sync.Once
	release = func() { once.Do(unlock) }
	t.Cleanup(release)
	return release
}

// clusterLockHeld reports whether some operation holds the per-cluster lock of id right now.
func clusterLockHeld(id int64) bool {
	v, ok := clusterLocks.Load(id)
	if !ok {
		return false
	}
	mu, ok := v.(*sync.Mutex)
	if !ok {
		return false
	}
	if mu.TryLock() {
		mu.Unlock()
		return false
	}
	return true
}

// credUpdateAsync runs Update in a goroutine of its own. resp may be read once done is closed.
func credUpdateAsync(c *client.Client, st tfsdk.State, plan tfsdk.Plan) (resp *resource.UpdateResponse, done <-chan struct{}) {
	resp = &resource.UpdateResponse{State: st}
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		(&K8sClusterResource{c: c}).Update(ctx, resource.UpdateRequest{State: st, Plan: plan}, resp)
	}()
	return resp, ch
}

func TestUpdate_FlagOnlyChangeDoesNotWaitForTheClusterLock(t *testing.T) {
	c, panel := newCredPanel(t)
	st, plan := credStateAndPlan(t, "null", "true", true)
	release := holdClusterLock(t, 313)

	resp, done := credUpdateAsync(c, st, plan)
	// Registered last, so it runs first: free the lock, let a stuck update finish while the
	// fake panel is still up.
	t.Cleanup(func() { release(); <-done })

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the update is queued behind the cluster lock though it changes nothing on the cluster")
	}
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	assertCredentialsInState(t, resp.State, true)
	if got := panel.requestsSeen(); !slices.Equal(got, []string{credGetCluster}) {
		t.Errorf("requests = %v, want just the one read", got)
	}
}

func TestUpdate_VersionChangeIsSerializedWithOtherClusterOperations(t *testing.T) {
	c, panel := newCredPanel(t)
	st, plan := credStateAndPlan(t, "null", "null", true)
	plan = credPlanWithVersion(t, plan, "v1.32.0")
	release := holdClusterLock(t, 313)

	// The upgrade reads the cluster three times: the mutability check, the wait for it to
	// settle, and the read-back that fills in state. Is the lock still held at the last?
	var lockedAtReadBack atomic.Bool
	panel.watchReads(func(n int) {
		if n == 3 {
			lockedAtReadBack.Store(clusterLockHeld(313))
		}
	})

	resp, done := credUpdateAsync(c, st, plan)
	t.Cleanup(func() { release(); <-done })

	// While another operation holds the cluster the upgrade does not so much as look at it.
	select {
	case <-done:
		t.Fatal("the upgrade ran while another operation held the cluster")
	case <-time.After(300 * time.Millisecond):
	}
	if got := panel.requestsSeen(); len(got) != 0 {
		t.Errorf("requests made while the cluster was held = %v, want none", got)
	}

	release()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the upgrade did not go ahead once the cluster was free")
	}
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	want := []string{credGetCluster, credUpgradeCluster, credGetCluster, credGetCluster}
	if got := panel.requestsSeen(); !slices.Equal(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
	if !lockedAtReadBack.Load() {
		t.Error("the cluster lock must be held until the cluster has been read back")
	}
	if clusterLockHeld(313) {
		t.Error("the cluster lock must be released when the update returns")
	}
}
