package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Protocol-level tests of the prodata_kubernetes_kubeconfig ephemeral resource. They drive
// the provider through the interface Terraform itself uses (GetProviderSchema,
// ConfigureProvider, OpenEphemeralResource), so they cover what the unit tests of the
// resource cannot: that the resource is registered, that the provider hands it the
// configured client, and that the schema Terraform receives marks every connection value
// sensitive. No credentials, no network: the panel is an httptest server.

const ephTypeName = "prodata_kubernetes_kubeconfig"

const (
	ephHost  = "https://k8s.example.com:6443"
	ephToken = "FAKE-BEARER-TOKEN-0123"
)

func ephB64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

var (
	ephCA   = ephB64("FAKE-CA-PEM")
	ephCert = ephB64("FAKE-CLIENT-CERT-PEM")
	ephKey  = ephB64("FAKE-CLIENT-KEY-PEM")
)

func ephCertKubeconfig() string {
	return `apiVersion: v1
kind: Config
current-context: admin@tf-cluster
clusters:
- name: tf-cluster
  cluster:
    server: ` + ephHost + `
    certificate-authority-data: ` + ephCA + `
contexts:
- name: admin@tf-cluster
  context:
    cluster: tf-cluster
    user: admin
users:
- name: admin
  user:
    client-certificate-data: ` + ephCert + `
    client-key-data: ` + ephKey + `
`
}

func ephTokenKubeconfig() string {
	return `apiVersion: v1
kind: Config
current-context: sa@tf-cluster
clusters:
- name: tf-cluster
  cluster:
    server: ` + ephHost + `
    certificate-authority-data: ` + ephCA + `
contexts:
- name: sa@tf-cluster
  context:
    cluster: tf-cluster
    user: sa
users:
- name: sa
  user:
    token: ` + ephToken + `
`
}

// ephNoServerKubeconfig parses but names no API server: a complete client-certificate pair
// that the current-context reaches, and nowhere to use it. Nothing but the missing address
// can make Open refuse it.
func ephNoServerKubeconfig() string {
	return `apiVersion: v1
kind: Config
current-context: admin@tf-cluster
clusters:
- name: tf-cluster
  cluster:
    certificate-authority-data: ` + ephCA + `
contexts:
- name: admin@tf-cluster
  context:
    cluster: tf-cluster
    user: admin
users:
- name: admin
  user:
    client-certificate-data: ` + ephCert + `
    client-key-data: ` + ephKey + `
`
}

// ephNoUserKubeconfig has an API server, but its context names a user the file does not
// define (the one it does define is unused): a host and nothing to authenticate with.
func ephNoUserKubeconfig() string {
	return `apiVersion: v1
kind: Config
current-context: admin@tf-cluster
clusters:
- name: tf-cluster
  cluster:
    server: ` + ephHost + `
    certificate-authority-data: ` + ephCA + `
contexts:
- name: admin@tf-cluster
  context:
    cluster: tf-cluster
    user: kubernetes-admin
users:
- name: admin
  user:
    client-certificate-data: ` + ephCert + `
    client-key-data: ` + ephKey + `
`
}

// ephCertNoKeyKubeconfig has an API server and half of a client-certificate pair.
func ephCertNoKeyKubeconfig() string {
	return `apiVersion: v1
kind: Config
current-context: admin@tf-cluster
clusters:
- name: tf-cluster
  cluster:
    server: ` + ephHost + `
    certificate-authority-data: ` + ephCA + `
contexts:
- name: admin@tf-cluster
  context:
    cluster: tf-cluster
    user: admin
users:
- name: admin
  user:
    client-certificate-data: ` + ephCert + `
`
}

// ephLeakMarkers must appear in no diagnostic: every secret in decoded and encoded form,
// the API server address, and the kubeconfig documents as the panel hands them over.
var ephLeakMarkers = []string{
	ephCA, ephCert, ephKey, ephToken, ephHost,
	"FAKE-CA-PEM", "FAKE-CLIENT-CERT-PEM", "FAKE-CLIENT-KEY-PEM",
	ephB64(ephCertKubeconfig()), ephB64(ephTokenKubeconfig()), ephB64(ephNoServerKubeconfig()),
	ephB64(ephNoUserKubeconfig()), ephB64(ephCertNoKeyKubeconfig()),
}

// ---- fake panel-main ----------------------------------------------------------------

type ephPanel struct {
	mu     sync.Mutex
	reply  func(w http.ResponseWriter, r *http.Request)
	paths  []string
	header []http.Header
	url    string
}

func newEphPanel(t *testing.T, reply func(w http.ResponseWriter, r *http.Request)) *ephPanel {
	t.Helper()
	p := &ephPanel{reply: reply}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.paths = append(p.paths, r.Method+" "+r.URL.Path)
		p.header = append(p.header, r.Header.Clone())
		p.mu.Unlock()
		p.reply(w, r)
	}))
	t.Cleanup(srv.Close)
	p.url = srv.URL
	return p
}

func (p *ephPanel) seen() ([]string, []http.Header) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.paths...), append([]http.Header(nil), p.header...)
}

// ephCluster answers getCluster/313 with the given status and clusterConfigSecret.
func ephCluster(t *testing.T, status, secret string) func(w http.ResponseWriter, r *http.Request) {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/panel-main/api/kubernetes/getCluster/313" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": 0, "errMessage": nil,
			"data": map[string]any{
				"id": 313, "name": "tf-cluster", "status": map[string]any{"name": status},
				"clusterConfigSecret": secret,
			},
		})
	}
}

func ephFailure(status int, body string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// ---- protocol plumbing --------------------------------------------------------------

type ephServer struct {
	t      *testing.T
	srv    tfprotov6.ProviderServer
	schema *tfprotov6.GetProviderSchemaResponse
}

func newEphServer(t *testing.T) *ephServer {
	t.Helper()
	srv := providerserver.NewProtocol6(New("test")())()
	schema, err := srv.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("GetProviderSchema: %v", err)
	}
	if d := ephErrors(schema.Diagnostics); d != "" {
		t.Fatalf("GetProviderSchema diagnostics: %s", d)
	}
	return &ephServer{t: t, srv: srv, schema: schema}
}

func ephErrors(diags []*tfprotov6.Diagnostic) string {
	var sb strings.Builder
	for _, d := range diags {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			sb.WriteString(d.Summary + ": " + d.Detail + "\n")
		}
	}
	return sb.String()
}

func ephDiagText(diags []*tfprotov6.Diagnostic) string {
	var sb strings.Builder
	for _, d := range diags {
		sb.WriteString(d.Summary + "\n" + d.Detail + "\n")
	}
	return sb.String()
}

// dynamic builds a DynamicValue of the given object type with every attribute null except
// the ones passed in.
func ephDynamic(t *testing.T, typ tftypes.Type, set map[string]tftypes.Value) *tfprotov6.DynamicValue {
	t.Helper()
	obj, ok := typ.(tftypes.Object)
	if !ok {
		t.Fatalf("not an object type: %T", typ)
	}
	vals := make(map[string]tftypes.Value, len(obj.AttributeTypes))
	for k, at := range obj.AttributeTypes {
		vals[k] = tftypes.NewValue(at, nil)
	}
	for k, v := range set {
		if _, known := vals[k]; !known {
			t.Fatalf("%q is not an attribute", k)
		}
		vals[k] = v
	}
	dv, err := tfprotov6.NewDynamicValue(typ, tftypes.NewValue(typ, vals))
	if err != nil {
		t.Fatalf("NewDynamicValue: %v", err)
	}
	return &dv
}

func ephStr(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }

// configure runs ConfigureProvider against the fake panel, the way `terraform plan` would
// with a provider block carrying these values.
func (s *ephServer) configure(panelURL string) {
	s.t.Helper()
	cfg := ephDynamic(s.t, s.schema.Provider.ValueType(), map[string]tftypes.Value{
		"api_base_url":   ephStr(panelURL),
		"api_key_id":     ephStr("prov-key"),
		"api_secret_key": ephStr("prov-secret"),
		"region":         ephStr("PROV-REGION"),
		"project_tag":    ephStr("prov-project"),
	})
	resp, err := s.srv.ConfigureProvider(context.Background(), &tfprotov6.ConfigureProviderRequest{
		TerraformVersion: "1.10.0", Config: cfg,
	})
	if err != nil {
		s.t.Fatalf("ConfigureProvider: %v", err)
	}
	if d := ephErrors(resp.Diagnostics); d != "" {
		s.t.Fatalf("ConfigureProvider diagnostics: %s", d)
	}
}

func (s *ephServer) ephemeralType() tftypes.Type {
	s.t.Helper()
	sch, ok := s.schema.EphemeralResourceSchemas[ephTypeName]
	if !ok {
		s.t.Fatalf("ephemeral resource %q is not registered", ephTypeName)
	}
	return sch.ValueType()
}

func (s *ephServer) open(set map[string]tftypes.Value) *tfprotov6.OpenEphemeralResourceResponse {
	s.t.Helper()
	resp, err := s.srv.OpenEphemeralResource(context.Background(), &tfprotov6.OpenEphemeralResourceRequest{
		TypeName: ephTypeName,
		Config:   ephDynamic(s.t, s.ephemeralType(), set),
	})
	if err != nil {
		s.t.Fatalf("OpenEphemeralResource: %v", err)
	}
	return resp
}

func ephClusterID(id int64) map[string]tftypes.Value {
	return map[string]tftypes.Value{"cluster_id": tftypes.NewValue(tftypes.Number, id)}
}

// result decodes an Open result into its attributes. A nil Result (an error with nothing to
// return) decodes to nil.
func (s *ephServer) result(resp *tfprotov6.OpenEphemeralResourceResponse) map[string]tftypes.Value {
	s.t.Helper()
	if resp.Result == nil {
		return nil
	}
	v, err := resp.Result.Unmarshal(s.ephemeralType())
	if err != nil {
		s.t.Fatalf("decode result: %v", err)
	}
	var m map[string]tftypes.Value
	if err := v.As(&m); err != nil {
		s.t.Fatalf("result is not an object: %v", err)
	}
	return m
}

// str returns the string attribute and whether it is set (non-null).
func ephAttr(t *testing.T, m map[string]tftypes.Value, name string) (string, bool) {
	t.Helper()
	v, ok := m[name]
	if !ok {
		t.Fatalf("result has no attribute %q", name)
	}
	if v.IsNull() {
		return "", false
	}
	if !v.IsKnown() {
		t.Fatalf("attribute %q is unknown in an Open result", name)
	}
	var s string
	if err := v.As(&s); err != nil {
		t.Fatalf("attribute %q is not a string: %v", name, err)
	}
	return s, true
}

var ephConnectionAttrs = []string{"host", "cluster_ca_certificate", "client_certificate", "client_key", "token", "raw_config"}

// ---- tests --------------------------------------------------------------------------

// The schema Terraform receives: the resource is registered as an ephemeral resource only,
// cluster_id is the one required input, and every connection value is Computed and
// Sensitive on the wire (that is what keeps them redacted in plan output and logs).
func TestProviderServer_EphemeralKubeconfig_Schema(t *testing.T) {
	s := newEphServer(t)

	sch, ok := s.schema.EphemeralResourceSchemas[ephTypeName]
	if !ok {
		t.Fatalf("ephemeral resource %q is not registered", ephTypeName)
	}
	if _, dup := s.schema.ResourceSchemas[ephTypeName]; dup {
		t.Errorf("%q must not also be a managed resource", ephTypeName)
	}
	if _, dup := s.schema.DataSourceSchemas[ephTypeName]; dup {
		t.Errorf("%q must not also be a data source", ephTypeName)
	}

	attrs := map[string]*tfprotov6.SchemaAttribute{}
	for _, a := range sch.Block.Attributes {
		attrs[a.Name] = a
	}
	if a := attrs["cluster_id"]; a == nil || !a.Required || a.Computed || a.Sensitive {
		t.Errorf("cluster_id must be a required, non-sensitive input: %+v", a)
	}
	for _, name := range []string{"region", "project_tag"} {
		if a := attrs[name]; a == nil || !a.Optional || a.Computed || a.Sensitive {
			t.Errorf("%s must be an optional, non-sensitive input: %+v", name, a)
		}
	}
	for _, name := range ephConnectionAttrs {
		if a := attrs[name]; a == nil || !a.Computed || !a.Sensitive || a.Required || a.Optional {
			t.Errorf("%s must be computed and sensitive, not an input: %+v", name, a)
		}
	}
	if len(attrs) != 3+len(ephConnectionAttrs) {
		t.Errorf("unexpected attributes: got %d, want %d", len(attrs), 3+len(ephConnectionAttrs))
	}
}

// Terraform validates the configuration before opening anything.
func TestProviderServer_EphemeralKubeconfig_ValidateConfig(t *testing.T) {
	s := newEphServer(t)
	typ := s.ephemeralType()

	tests := []struct {
		name    string
		set     map[string]tftypes.Value
		wantErr bool
	}{
		{"valid", ephClusterID(313), false},
		{"id not known yet", map[string]tftypes.Value{"cluster_id": tftypes.NewValue(tftypes.Number, tftypes.UnknownValue)}, false},
		{"zero id", ephClusterID(0), true},
		{"negative id", ephClusterID(-5), true},
		{"no id", map[string]tftypes.Value{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := s.srv.ValidateEphemeralResourceConfig(context.Background(), &tfprotov6.ValidateEphemeralResourceConfigRequest{
				TypeName: ephTypeName, Config: ephDynamic(t, typ, tt.set),
			})
			if err != nil {
				t.Fatalf("ValidateEphemeralResourceConfig: %v", err)
			}
			if got := ephErrors(resp.Diagnostics) != ""; got != tt.wantErr {
				t.Errorf("error = %v, want %v (diagnostics: %s)", got, tt.wantErr, ephDiagText(resp.Diagnostics))
			}
		})
	}
}

// Open through the provider: the client the provider built from its own configuration is
// the one the resource calls the panel with, a per-resource region / project_tag overrides
// it, and nothing is left to renew or close.
func TestProviderServer_EphemeralKubeconfig_Open(t *testing.T) {
	tests := []struct {
		name       string
		kubeconfig string
		set        map[string]tftypes.Value
		wantRegion string
		wantTag    string
		want       map[string]string // attribute -> value; absent attributes must be null
	}{
		{
			name:       "certificate auth, provider scope",
			kubeconfig: ephCertKubeconfig(),
			set:        ephClusterID(313),
			wantRegion: "PROV-REGION", wantTag: "prov-project",
			want: map[string]string{
				"host": ephHost, "cluster_ca_certificate": ephCA, "client_certificate": ephCert,
				"client_key": ephKey, "raw_config": ephCertKubeconfig(),
			},
		},
		{
			name:       "token auth, scope overrides",
			kubeconfig: ephTokenKubeconfig(),
			set: map[string]tftypes.Value{
				"cluster_id":  tftypes.NewValue(tftypes.Number, 313),
				"region":      ephStr("OVR-REGION"),
				"project_tag": ephStr("ovr-project"),
			},
			wantRegion: "OVR-REGION", wantTag: "ovr-project",
			want: map[string]string{
				"host": ephHost, "cluster_ca_certificate": ephCA, "token": ephToken,
				"raw_config": ephTokenKubeconfig(),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panel := newEphPanel(t, ephCluster(t, "SUCCESS", ephB64(tt.kubeconfig)))
			s := newEphServer(t)
			s.configure(panel.url)

			resp := s.open(tt.set)
			if len(resp.Diagnostics) != 0 {
				t.Fatalf("unexpected diagnostics: %s", ephDiagText(resp.Diagnostics))
			}
			if !resp.RenewAt.IsZero() || len(resp.Private) != 0 {
				t.Errorf("nothing should be scheduled for renewal or close: renewAt=%v private=%q", resp.RenewAt, resp.Private)
			}

			got := s.result(resp)
			if got == nil {
				t.Fatal("no result")
			}
			for _, name := range ephConnectionAttrs {
				val, set := ephAttr(t, got, name)
				want, wantSet := tt.want[name]
				if set != wantSet || val != want {
					t.Errorf("%s = (%q, set=%v), want (%q, set=%v)", name, val, set, want, wantSet)
				}
			}
			// The inputs come back exactly as configured.
			var id big.Float
			if err := got["cluster_id"].As(&id); err != nil || id.Cmp(big.NewFloat(313)) != 0 {
				t.Errorf("cluster_id = %v (%v), want 313", got["cluster_id"], err)
			}

			paths, headers := panel.seen()
			if len(paths) != 1 || paths[0] != "GET /panel-main/api/kubernetes/getCluster/313" {
				t.Fatalf("panel requests = %v, want exactly one getCluster/313", paths)
			}
			h := headers[0]
			if h.Get("X-API-KEY") != "prov-key" || h.Get("X-API-SECRET") != "prov-secret" {
				t.Errorf("the provider's credentials did not reach the API call: key=%q", h.Get("X-API-KEY"))
			}
			if h.Get("X-Region") != tt.wantRegion || h.Get("X-Project-Tag") != tt.wantTag {
				t.Errorf("scope = %q / %q, want %q / %q", h.Get("X-Region"), h.Get("X-Project-Tag"), tt.wantRegion, tt.wantTag)
			}

			closeResp, err := s.srv.CloseEphemeralResource(context.Background(), &tfprotov6.CloseEphemeralResourceRequest{
				TypeName: ephTypeName, Private: resp.Private,
			})
			if err != nil {
				t.Fatalf("CloseEphemeralResource: %v", err)
			}
			if len(closeResp.Diagnostics) != 0 {
				t.Errorf("Close diagnostics: %s", ephDiagText(closeResp.Diagnostics))
			}
		})
	}
}

// Whenever usable credentials are missing, Open reports an error and returns no connection
// value — a null host would let a kubernetes / helm provider fall back to other connection
// settings it can find (config_path, the pod's service account) — and nothing derived from
// the kubeconfig reaches a message.
func TestProviderServer_EphemeralKubeconfig_FailsClosed(t *testing.T) {
	// Two different checks share the summary "Kubeconfig could not be used"; the detail tells
	// them apart, so a row proves which check refused it.
	const (
		noAddress     = "no API server address"
		noCredentials = "neither a client certificate with its key nor a token"
	)
	tests := []struct {
		name        string
		reply       func(t *testing.T) func(w http.ResponseWriter, r *http.Request)
		wantSummary string
		wantDetail  string
	}{
		{"cluster not found", func(*testing.T) func(http.ResponseWriter, *http.Request) {
			return ephFailure(http.StatusInternalServerError, `{"error":500,"errMessage":"Cluster not found!","data":null}`)
		}, "Kubernetes cluster not found", "313"},
		{"cluster deleted", func(t *testing.T) func(http.ResponseWriter, *http.Request) {
			return ephCluster(t, "DELETED", ephB64(ephCertKubeconfig()))
		}, "Kubernetes cluster is deleted", "313"},
		{"kubeconfig not produced yet", func(t *testing.T) func(http.ResponseWriter, *http.Request) {
			return ephCluster(t, "PROCESSING", "")
		}, "Kubeconfig is not available", "PROCESSING"},
		{"kubeconfig without a server", func(t *testing.T) func(http.ResponseWriter, *http.Request) {
			return ephCluster(t, "SUCCESS", ephB64(ephNoServerKubeconfig()))
		}, "Kubeconfig could not be used", noAddress},
		{"server but the context's user is not defined", func(t *testing.T) func(http.ResponseWriter, *http.Request) {
			return ephCluster(t, "SUCCESS", ephB64(ephNoUserKubeconfig()))
		}, "Kubeconfig could not be used", noCredentials},
		{"server but a certificate without its key", func(t *testing.T) func(http.ResponseWriter, *http.Request) {
			return ephCluster(t, "SUCCESS", ephB64(ephCertNoKeyKubeconfig()))
		}, "Kubeconfig could not be used", noCredentials},
		{"panel failure", func(*testing.T) func(http.ResponseWriter, *http.Request) {
			return ephFailure(http.StatusInternalServerError, `{"error":500,"errMessage":"boom","data":null}`)
		}, "Unable to read Kubernetes cluster", "boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panel := newEphPanel(t, tt.reply(t))
			s := newEphServer(t)
			s.configure(panel.url)

			resp := s.open(ephClusterID(313))

			if ephErrors(resp.Diagnostics) == "" {
				t.Fatal("expected an error diagnostic, got none")
			}
			var first *tfprotov6.Diagnostic
			for _, d := range resp.Diagnostics {
				if d.Severity == tfprotov6.DiagnosticSeverityError {
					first = d
					break
				}
			}
			if first.Summary != tt.wantSummary {
				t.Errorf("summary = %q, want %q (detail: %s)", first.Summary, tt.wantSummary, first.Detail)
			}
			if !strings.Contains(first.Detail, tt.wantDetail) {
				t.Errorf("detail %q does not mention %q", first.Detail, tt.wantDetail)
			}
			text := ephDiagText(resp.Diagnostics)
			for _, m := range ephLeakMarkers {
				if strings.Contains(text, m) {
					t.Errorf("diagnostics contain %q:\n%s", m, text)
				}
			}
			if got := s.result(resp); got != nil {
				for _, name := range ephConnectionAttrs {
					if v, set := ephAttr(t, got, name); set {
						t.Errorf("%s = %q on a failed Open; it must stay null", name, v)
					}
				}
			}
		})
	}
}

// A provider that was never configured has no client: Open must say so rather than
// dereference nil, and must not reach the panel.
func TestProviderServer_EphemeralKubeconfig_NotConfigured(t *testing.T) {
	panel := newEphPanel(t, ephCluster(t, "SUCCESS", ephB64(ephCertKubeconfig())))
	s := newEphServer(t) // no ConfigureProvider

	resp := s.open(ephClusterID(313))

	if len(resp.Diagnostics) == 0 || resp.Diagnostics[0].Summary != "Provider is not configured" {
		t.Fatalf("want a 'Provider is not configured' error, got: %s", ephDiagText(resp.Diagnostics))
	}
	if paths, _ := panel.seen(); len(paths) != 0 {
		t.Errorf("the panel must not be called, saw %v", paths)
	}
}
