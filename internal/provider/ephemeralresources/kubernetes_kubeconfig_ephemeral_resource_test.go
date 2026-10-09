package ephemeralresources

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-log/tflogtest"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// Distinct, greppable markers. If one turns up in a diagnostic or a log line, a secret
// (or something derived from the kubeconfig) leaked there.
var (
	markerCA   = b64("FAKE-CA-PEM")
	markerCert = b64("FAKE-CLIENT-CERT-PEM")
	markerKey  = b64("FAKE-CLIENT-KEY-PEM")
)

const (
	markerToken = "FAKE-BEARER-TOKEN-0123"
	fixtureHost = "https://k8s.example.com:6443"

	// What the credential-less kubeconfigs below use instead of embedded credentials.
	fixtureCertFile    = "/home/admin/.kube/fake-admin.crt"
	fixtureKeyFile     = "/home/admin/.kube/fake-admin.key"
	fixtureExecCommand = "fake-auth-helper"

	// unparsedKubeconfig is a secret the lenient parser cannot read; the resource sees it
	// only as text, which is exactly the text that must not end up in a message.
	unparsedKubeconfig = "{ this is: [not, a kubeconfig"
)

// leakMarkers are the strings no diagnostic and no log line may contain: the decoded and
// the base64 form of every secret, the API server address, and each kubeconfig document as
// the panel hands it over (base64 of the whole file). That last form hides the markers
// inside it behind the outer encoding, so a log line carrying it would otherwise pass.
var leakMarkers = []string{
	markerCA, markerCert, markerKey, markerToken, fixtureHost,
	"FAKE-CA-PEM", "FAKE-CLIENT-CERT-PEM", "FAKE-CLIENT-KEY-PEM",
	b64(certKubeconfig()), b64(tokenKubeconfig()), b64(noServerKubeconfig()),
	unparsedKubeconfig, b64(unparsedKubeconfig),
	// Where the credential-less documents differ from the ones above.
	b64(userMissingKubeconfig()), b64(certWithoutKeyKubeconfig()), b64(keyWithoutCertKubeconfig()),
	b64(certFilesKubeconfig()), b64(execKubeconfig()),
	fixtureCertFile, fixtureKeyFile, fixtureExecCommand,
}

// certKubeconfig is a kubeconfig with client-certificate auth, the shape the panel
// produces for a managed cluster.
func certKubeconfig() string {
	return `apiVersion: v1
kind: Config
current-context: admin@tf-cluster
clusters:
- name: tf-cluster
  cluster:
    server: ` + fixtureHost + `
    certificate-authority-data: ` + markerCA + `
contexts:
- name: admin@tf-cluster
  context:
    cluster: tf-cluster
    user: admin
users:
- name: admin
  user:
    client-certificate-data: ` + markerCert + `
    client-key-data: ` + markerKey + `
`
}

// tokenKubeconfig authenticates with a bearer token and carries no client certificate.
func tokenKubeconfig() string {
	return `apiVersion: v1
kind: Config
current-context: sa@tf-cluster
clusters:
- name: tf-cluster
  cluster:
    server: ` + fixtureHost + `
    certificate-authority-data: ` + markerCA + `
contexts:
- name: sa@tf-cluster
  context:
    cluster: tf-cluster
    user: sa
users:
- name: sa
  user:
    token: ` + markerToken + `
`
}

// noServerKubeconfig parses but names no API server: a complete client-certificate pair
// that the current-context reaches, and nowhere to use it. Nothing but the missing address
// can make Open refuse it.
func noServerKubeconfig() string {
	return `apiVersion: v1
kind: Config
current-context: admin@tf-cluster
clusters:
- name: tf-cluster
  cluster:
    certificate-authority-data: ` + markerCA + `
contexts:
- name: admin@tf-cluster
  context:
    cluster: tf-cluster
    user: admin
users:
- name: admin
  user:
    client-certificate-data: ` + markerCert + `
    client-key-data: ` + markerKey + `
`
}

// kubeconfigWithUser is a kubeconfig with a well-formed cluster (an API server and its CA)
// and one context, which names contextUser; usersYAML becomes the users: list. The parser
// resolves the host, but whether any credentials come out depends on usersYAML alone.
func kubeconfigWithUser(contextUser, usersYAML string) string {
	return `apiVersion: v1
kind: Config
current-context: admin@tf-cluster
clusters:
- name: tf-cluster
  cluster:
    server: ` + fixtureHost + `
    certificate-authority-data: ` + markerCA + `
contexts:
- name: admin@tf-cluster
  context:
    cluster: tf-cluster
    user: ` + contextUser + `
users:
` + usersYAML
}

// The kubeconfigs below all have an API server and none has a way to authenticate that the
// parser can read, so ParseKubeConfig returns a host with no usable credentials.

// userMissingKubeconfig: the context names a user the file does not define. (The user it
// does define is unused, and its credentials must not be picked up.)
func userMissingKubeconfig() string {
	return kubeconfigWithUser("kubernetes-admin", `- name: admin
  user:
    client-certificate-data: `+markerCert+`
    client-key-data: `+markerKey+`
`)
}

// certWithoutKeyKubeconfig: half of a client-certificate pair.
func certWithoutKeyKubeconfig() string {
	return kubeconfigWithUser("admin", `- name: admin
  user:
    client-certificate-data: `+markerCert+`
`)
}

// keyWithoutCertKubeconfig: the other half.
func keyWithoutCertKubeconfig() string {
	return kubeconfigWithUser("admin", `- name: admin
  user:
    client-key-data: `+markerKey+`
`)
}

// certFilesKubeconfig: the certificate and key are files on the machine that wrote the
// kubeconfig, not embedded data.
func certFilesKubeconfig() string {
	return kubeconfigWithUser("admin", `- name: admin
  user:
    client-certificate: `+fixtureCertFile+`
    client-key: `+fixtureKeyFile+`
`)
}

// execKubeconfig: credentials come from a command the client would have to run.
func execKubeconfig() string {
	return kubeconfigWithUser("admin", `- name: admin
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1
      command: `+fixtureExecCommand+`
`)
}

// clusterReply is a getCluster success envelope whose clusterConfigSecret is secret.
func clusterReply(t *testing.T, status, secret string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"error":      0,
		"errMessage": nil,
		"data": map[string]any{
			"id":                  313,
			"name":                "tf-cluster",
			"status":              map[string]any{"name": status},
			"clusterConfigSecret": secret,
		},
	})
	if err != nil {
		t.Fatalf("marshal cluster reply: %v", err)
	}
	return string(b)
}

func replyWith(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

type seenRequest struct {
	Method string
	Path   string
	Header http.Header
}

// fakePanel is an httptest server standing in for panel-main, plus a client pointed at
// it and a record of what the client sent.
type fakePanel struct {
	mu   sync.Mutex
	seen []seenRequest
	c    *client.Client
}

func newFakePanel(t *testing.T, handler http.HandlerFunc) *fakePanel {
	t.Helper()
	p := &fakePanel{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.seen = append(p.seen, seenRequest{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone()})
		p.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(client.Config{
		APIBaseURL:   srv.URL,
		APIKeyID:     "test-key",
		APISecretKey: "test-secret",
		Region:       "TEST",
		ProjectTag:   "test-project",
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	p.c = c
	return p
}

func (p *fakePanel) requests() []seenRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]seenRequest(nil), p.seen...)
}

func (p *fakePanel) open(ctx context.Context, t *testing.T, set map[string]tftypes.Value) ephemeral.OpenResponse {
	t.Helper()
	return openResource(ctx, t, &K8sKubeconfigEphemeralResource{c: p.c}, set)
}

func kubeconfigSchema(t *testing.T) schema.Schema {
	t.Helper()
	var resp ephemeral.SchemaResponse
	NewK8sKubeconfigEphemeralResource().Schema(context.Background(), ephemeral.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	return resp.Schema
}

// kubeconfigConfig builds a tfsdk.Config in which every attribute is null except the
// ones passed in.
func kubeconfigConfig(t *testing.T, set map[string]tftypes.Value) tfsdk.Config {
	t.Helper()
	s := kubeconfigSchema(t)
	objType, ok := s.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("schema type is not an object")
	}
	vals := make(map[string]tftypes.Value, len(objType.AttributeTypes))
	for k, typ := range objType.AttributeTypes {
		vals[k] = tftypes.NewValue(typ, nil)
	}
	for k, v := range set {
		if _, known := vals[k]; !known {
			t.Fatalf("config sets %q, which is not a schema attribute", k)
		}
		vals[k] = v
	}
	return tfsdk.Config{Schema: s, Raw: tftypes.NewValue(objType, vals)}
}

// openResource runs Open the way the framework does: the response's Result starts out as
// a copy of the configuration.
func openResource(ctx context.Context, t *testing.T, e *K8sKubeconfigEphemeralResource, set map[string]tftypes.Value) ephemeral.OpenResponse {
	t.Helper()
	cfg := kubeconfigConfig(t, set)
	resp := ephemeral.OpenResponse{Result: tfsdk.EphemeralResultData{Schema: cfg.Schema, Raw: cfg.Raw.Copy()}}
	e.Open(ctx, ephemeral.OpenRequest{Config: cfg}, &resp)
	return resp
}

// clusterIDOnly is a configuration that sets nothing but cluster_id, to the fixture
// cluster's id.
func clusterIDOnly() map[string]tftypes.Value {
	return map[string]tftypes.Value{"cluster_id": tftypes.NewValue(tftypes.Number, 313)}
}

func resultOf(t *testing.T, resp ephemeral.OpenResponse) K8sKubeconfigModel {
	t.Helper()
	var m K8sKubeconfigModel
	if diags := resp.Result.Get(context.Background(), &m); diags.HasError() {
		t.Fatalf("read result: %v", diags)
	}
	return m
}

func diagText(resp ephemeral.OpenResponse) string {
	var sb strings.Builder
	for _, d := range resp.Diagnostics {
		sb.WriteString(d.Summary())
		sb.WriteString("\n")
		sb.WriteString(d.Detail())
		sb.WriteString("\n")
	}
	return sb.String()
}

func assertNoLeak(t *testing.T, what, text string) {
	t.Helper()
	for _, m := range leakMarkers {
		if strings.Contains(text, m) {
			t.Errorf("%s contains %q:\n%s", what, m, text)
		}
	}
}

// assertFailedClosed checks the fail-closed contract: an error diagnostic with the
// expected summary, nothing from the kubeconfig in any message, and not one connection
// attribute set on the result.
func assertFailedClosed(t *testing.T, resp ephemeral.OpenResponse, wantSummary string) {
	t.Helper()
	errs := resp.Diagnostics.Errors()
	if len(errs) == 0 {
		t.Fatal("expected an error diagnostic, got none")
	}
	if errs[0].Summary() != wantSummary {
		t.Errorf("summary = %q, want %q (detail: %s)", errs[0].Summary(), wantSummary, errs[0].Detail())
	}
	assertNoLeak(t, "diagnostics", diagText(resp))

	got := resultOf(t, resp)
	for name, v := range map[string]types.String{
		"host": got.Host, "cluster_ca_certificate": got.ClusterCACertificate,
		"client_certificate": got.ClientCertificate, "client_key": got.ClientKey,
		"token": got.Token, "raw_config": got.RawConfig,
	} {
		if !v.IsNull() {
			t.Errorf("result.%s must stay null when Open fails", name)
		}
	}
}

func modelTags(typ reflect.Type) map[string]bool {
	tags := make(map[string]bool)
	for i := 0; i < typ.NumField(); i++ {
		if tag := typ.Field(i).Tag.Get("tfsdk"); tag != "" && tag != "-" {
			tags[tag] = true
		}
	}
	return tags
}

func TestK8sKubeconfig_Metadata(t *testing.T) {
	var resp ephemeral.MetadataResponse
	NewK8sKubeconfigEphemeralResource().Metadata(context.Background(),
		ephemeral.MetadataRequest{ProviderTypeName: "prodata"}, &resp)
	if resp.TypeName != "prodata_kubernetes_kubeconfig" {
		t.Errorf("TypeName = %q, want prodata_kubernetes_kubeconfig", resp.TypeName)
	}
}

func TestK8sKubeconfig_SchemaMatchesModel(t *testing.T) {
	s := kubeconfigSchema(t)
	tags := modelTags(reflect.TypeOf(K8sKubeconfigModel{}))
	for name := range s.Attributes {
		if !tags[name] {
			t.Errorf("schema attribute %q has no matching model field", name)
		}
	}
	for tag := range tags {
		if _, ok := s.Attributes[tag]; !ok {
			t.Errorf("model tfsdk tag %q has no matching schema attribute", tag)
		}
	}
}

// TestK8sKubeconfig_SchemaShape pins the properties the feature stands on: every
// credential output is Computed and Sensitive (redacted wherever Terraform prints it) and
// can never be supplied by the user, and the lookup key is validated.
func TestK8sKubeconfig_SchemaShape(t *testing.T) {
	s := kubeconfigSchema(t)

	id, ok := s.Attributes["cluster_id"].(schema.Int64Attribute)
	if !ok || !id.Required || id.Computed || id.Sensitive {
		t.Fatalf("cluster_id must be a Required, non-Computed Int64: %+v", s.Attributes["cluster_id"])
	}
	for _, tc := range []struct {
		val     int64
		wantErr bool
	}{{-1, true}, {0, true}, {1, false}, {313, false}} {
		var resp validator.Int64Response
		for _, v := range id.Validators {
			v.ValidateInt64(context.Background(), validator.Int64Request{ConfigValue: types.Int64Value(tc.val)}, &resp)
		}
		if got := resp.Diagnostics.HasError(); got != tc.wantErr {
			t.Errorf("cluster_id=%d: validator error = %v, want %v", tc.val, got, tc.wantErr)
		}
	}

	for _, name := range []string{"region", "project_tag"} {
		a, ok := s.Attributes[name].(schema.StringAttribute)
		if !ok || !a.Optional || a.Required || a.Computed {
			t.Errorf("%s must be Optional only: %+v", name, s.Attributes[name])
		}
	}

	outputs := []string{"host", "cluster_ca_certificate", "client_certificate", "client_key", "token", "raw_config"}
	for _, name := range outputs {
		a, ok := s.Attributes[name].(schema.StringAttribute)
		if !ok {
			t.Errorf("%s must be a String attribute: %+v", name, s.Attributes[name])
			continue
		}
		if !a.Computed || !a.Sensitive || a.Optional || a.Required {
			t.Errorf("%s must be Computed+Sensitive and not user-settable: %+v", name, a)
		}
	}
	if want := 3 + len(outputs); len(s.Attributes) != want {
		t.Errorf("schema has %d attributes, want %d — a new attribute needs a decision on Sensitive", len(s.Attributes), want)
	}
}

func TestK8sKubeconfig_Configure(t *testing.T) {
	ctx := context.Background()

	t.Run("no provider data is a no-op", func(t *testing.T) {
		e := &K8sKubeconfigEphemeralResource{}
		var resp ephemeral.ConfigureResponse
		e.Configure(ctx, ephemeral.ConfigureRequest{}, &resp)
		if resp.Diagnostics.HasError() || e.c != nil {
			t.Errorf("want no error and no client, got %v / %v", resp.Diagnostics, e.c)
		}
	})

	t.Run("wrong type is an error", func(t *testing.T) {
		e := &K8sKubeconfigEphemeralResource{}
		var resp ephemeral.ConfigureResponse
		e.Configure(ctx, ephemeral.ConfigureRequest{ProviderData: "not a client"}, &resp)
		if !resp.Diagnostics.HasError() || e.c != nil {
			t.Errorf("want an error and no client, got %v / %v", resp.Diagnostics, e.c)
		}
	})

	t.Run("client is kept", func(t *testing.T) {
		p := newFakePanel(t, replyWith(http.StatusOK, "{}"))
		e := &K8sKubeconfigEphemeralResource{}
		var resp ephemeral.ConfigureResponse
		e.Configure(ctx, ephemeral.ConfigureRequest{ProviderData: p.c}, &resp)
		if resp.Diagnostics.HasError() || e.c != p.c {
			t.Errorf("want the client stored, got %v / %v", resp.Diagnostics, e.c)
		}
	})
}

func TestK8sKubeconfig_OpenCertificateAuth(t *testing.T) {
	p := newFakePanel(t, replyWith(http.StatusOK, clusterReply(t, "SUCCESS", b64(certKubeconfig()))))
	resp := p.open(context.Background(), t, clusterIDOnly())

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	reqs := p.requests()
	if len(reqs) != 1 || reqs[0].Method != http.MethodGet || reqs[0].Path != "/panel-main/api/kubernetes/getCluster/313" {
		t.Fatalf("want one GET of getCluster/313, got %+v", reqs)
	}
	if got := reqs[0].Header.Get("X-Region"); got != "TEST" {
		t.Errorf("X-Region = %q, want the provider default TEST", got)
	}
	if got := reqs[0].Header.Get("X-Project-Tag"); got != "test-project" {
		t.Errorf("X-Project-Tag = %q, want the provider default test-project", got)
	}

	got := resultOf(t, resp)
	if got.ClusterID.ValueInt64() != 313 {
		t.Errorf("cluster_id = %v, want it echoed back as 313", got.ClusterID)
	}
	if !got.Region.IsNull() || !got.ProjectTag.IsNull() {
		t.Errorf("region/project_tag were not configured and must stay null: %v / %v", got.Region, got.ProjectTag)
	}
	checks := []struct{ name, got, want string }{
		{"host", got.Host.ValueString(), fixtureHost},
		{"cluster_ca_certificate", got.ClusterCACertificate.ValueString(), markerCA},
		{"client_certificate", got.ClientCertificate.ValueString(), markerCert},
		{"client_key", got.ClientKey.ValueString(), markerKey},
		{"raw_config", got.RawConfig.ValueString(), certKubeconfig()},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	if !got.Token.IsNull() {
		t.Errorf("token = %v, want null for certificate auth", got.Token)
	}
}

func TestK8sKubeconfig_OpenTokenAuth(t *testing.T) {
	p := newFakePanel(t, replyWith(http.StatusOK, clusterReply(t, "SUCCESS", b64(tokenKubeconfig()))))
	resp := p.open(context.Background(), t, clusterIDOnly())

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	got := resultOf(t, resp)
	if got.Host.ValueString() != fixtureHost || got.Token.ValueString() != markerToken ||
		got.ClusterCACertificate.ValueString() != markerCA {
		t.Errorf("unexpected result: host=%v token=%v ca=%v", got.Host, got.Token, got.ClusterCACertificate)
	}
	if !got.ClientCertificate.IsNull() || !got.ClientKey.IsNull() {
		t.Errorf("client certificate/key must be null for token auth, got %v / %v", got.ClientCertificate, got.ClientKey)
	}
}

// The panel normally base64-wraps the kubeconfig, but the parser accepts plain YAML too
// and Open must behave the same either way.
func TestK8sKubeconfig_OpenPlainYAMLSecret(t *testing.T) {
	p := newFakePanel(t, replyWith(http.StatusOK, clusterReply(t, "SUCCESS", certKubeconfig())))
	resp := p.open(context.Background(), t, clusterIDOnly())

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	got := resultOf(t, resp)
	if got.Host.ValueString() != fixtureHost || got.RawConfig.ValueString() != certKubeconfig() {
		t.Errorf("plain-YAML secret not handled: host=%v", got.Host)
	}
}

func TestK8sKubeconfig_OpenScopeOverrides(t *testing.T) {
	p := newFakePanel(t, replyWith(http.StatusOK, clusterReply(t, "SUCCESS", b64(certKubeconfig()))))
	set := clusterIDOnly()
	set["region"] = tftypes.NewValue(tftypes.String, "OTHER-1")
	set["project_tag"] = tftypes.NewValue(tftypes.String, "other-project")
	resp := p.open(context.Background(), t, set)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	reqs := p.requests()
	if len(reqs) != 1 {
		t.Fatalf("want one request, got %d", len(reqs))
	}
	if got := reqs[0].Header.Get("X-Region"); got != "OTHER-1" {
		t.Errorf("X-Region = %q, want the override OTHER-1", got)
	}
	if got := reqs[0].Header.Get("X-Project-Tag"); got != "other-project" {
		t.Errorf("X-Project-Tag = %q, want the override other-project", got)
	}
	got := resultOf(t, resp)
	if got.Region.ValueString() != "OTHER-1" || got.ProjectTag.ValueString() != "other-project" {
		t.Errorf("region/project_tag must be echoed back unchanged: %v / %v", got.Region, got.ProjectTag)
	}
}

// A cluster is usable whenever the panel has a kubeconfig for it, whatever its status:
// a pool being resized or a failed upgrade must not lock an operator out of the cluster.
func TestK8sKubeconfig_OpenDoesNotGateOnStatus(t *testing.T) {
	for _, status := range []string{"SUCCESS", "PROCESSING", "FAIL", "DELETING"} {
		t.Run(status, func(t *testing.T) {
			p := newFakePanel(t, replyWith(http.StatusOK, clusterReply(t, status, b64(certKubeconfig()))))
			resp := p.open(context.Background(), t, clusterIDOnly())
			if resp.Diagnostics.HasError() {
				t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
			}
			if got := resultOf(t, resp); got.Host.ValueString() != fixtureHost {
				t.Errorf("host = %v, want %s", got.Host, fixtureHost)
			}
		})
	}
}

// noCredentialsDetail is how the "host but no credentials" error tells itself apart from the
// "no API server address" one.
const noCredentialsDetail = "neither a client certificate with its key nor a token"

func TestK8sKubeconfig_OpenFailsClosed(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		wantSummary string
		wantDetail  string
	}{
		{
			name:        "cluster not found (business error at HTTP 500)",
			status:      http.StatusInternalServerError,
			body:        `{"data":null,"errMessage":"Cluster not found!","error":500}`,
			wantSummary: "Kubernetes cluster not found",
			wantDetail:  "313",
		},
		{
			name:        "cluster not found (HTTP 404)",
			status:      http.StatusNotFound,
			body:        `{"data":null,"errMessage":"Not Found","error":404}`,
			wantSummary: "Kubernetes cluster not found",
			wantDetail:  "313",
		},
		{
			name:        "any other API failure",
			status:      http.StatusInternalServerError,
			body:        `{"data":null,"errMessage":"upstream exploded","error":500}`,
			wantSummary: "Unable to read Kubernetes cluster",
			wantDetail:  "upstream exploded",
		},
		{
			// The deleted cluster's reply still carries its credentials; they must not be
			// handed out.
			name:        "deleted cluster that still has a kubeconfig",
			status:      http.StatusOK,
			body:        clusterReply(t, "DELETED", b64(certKubeconfig())),
			wantSummary: "Kubernetes cluster is deleted",
			wantDetail:  "313",
		},
		{
			name:        "new cluster without a kubeconfig yet",
			status:      http.StatusOK,
			body:        clusterReply(t, "NEW", ""),
			wantSummary: "Kubeconfig is not available",
			wantDetail:  "NEW",
		},
		{
			name:        "processing cluster without a kubeconfig yet",
			status:      http.StatusOK,
			body:        clusterReply(t, "PROCESSING", ""),
			wantSummary: "Kubeconfig is not available",
			wantDetail:  "PROCESSING",
		},
		{
			name:        "whitespace-only kubeconfig",
			status:      http.StatusOK,
			body:        clusterReply(t, "SUCCESS", "  \n"),
			wantSummary: "Kubeconfig is not available",
			wantDetail:  "SUCCESS",
		},
		{
			name:        "kubeconfig without an API server address",
			status:      http.StatusOK,
			body:        clusterReply(t, "SUCCESS", b64(noServerKubeconfig())),
			wantSummary: "Kubeconfig could not be used",
			wantDetail:  "no API server address",
		},
		{
			name:        "kubeconfig that is not YAML",
			status:      http.StatusOK,
			body:        clusterReply(t, "SUCCESS", b64(unparsedKubeconfig)),
			wantSummary: "Kubeconfig could not be used",
			wantDetail:  "no API server address",
		},
		// A host with nothing to authenticate with is the unsafe case too: a kubernetes or
		// helm provider given null credentials may add its own from elsewhere.
		{
			name:        "API server but the context's user is not defined",
			status:      http.StatusOK,
			body:        clusterReply(t, "SUCCESS", b64(userMissingKubeconfig())),
			wantSummary: "Kubeconfig could not be used",
			wantDetail:  noCredentialsDetail,
		},
		{
			name:        "API server but a certificate without its key",
			status:      http.StatusOK,
			body:        clusterReply(t, "SUCCESS", b64(certWithoutKeyKubeconfig())),
			wantSummary: "Kubeconfig could not be used",
			wantDetail:  noCredentialsDetail,
		},
		{
			name:        "API server but a key without its certificate",
			status:      http.StatusOK,
			body:        clusterReply(t, "SUCCESS", b64(keyWithoutCertKubeconfig())),
			wantSummary: "Kubeconfig could not be used",
			wantDetail:  noCredentialsDetail,
		},
		{
			name:        "API server but certificate files instead of embedded data",
			status:      http.StatusOK,
			body:        clusterReply(t, "SUCCESS", b64(certFilesKubeconfig())),
			wantSummary: "Kubeconfig could not be used",
			wantDetail:  noCredentialsDetail,
		},
		{
			name:        "API server but an exec plugin",
			status:      http.StatusOK,
			body:        clusterReply(t, "SUCCESS", b64(execKubeconfig())),
			wantSummary: "Kubeconfig could not be used",
			wantDetail:  noCredentialsDetail,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newFakePanel(t, replyWith(tt.status, tt.body))
			resp := p.open(context.Background(), t, clusterIDOnly())

			assertFailedClosed(t, resp, tt.wantSummary)
			if detail := resp.Diagnostics.Errors()[0].Detail(); !strings.Contains(detail, tt.wantDetail) {
				t.Errorf("detail %q does not mention %q", detail, tt.wantDetail)
			}
		})
	}
}

func TestK8sKubeconfig_OpenGuards(t *testing.T) {
	failIfCalled := func(t *testing.T) http.HandlerFunc {
		return func(_ http.ResponseWriter, r *http.Request) {
			t.Errorf("the API must not be called, got %s %s", r.Method, r.URL.Path)
		}
	}

	t.Run("provider not configured", func(t *testing.T) {
		resp := openResource(context.Background(), t, &K8sKubeconfigEphemeralResource{}, clusterIDOnly())
		assertFailedClosed(t, resp, "Provider is not configured")
	})

	t.Run("cluster_id null", func(t *testing.T) {
		p := newFakePanel(t, failIfCalled(t))
		resp := p.open(context.Background(), t, nil)
		assertFailedClosed(t, resp, "cluster_id is not known")
	})

	t.Run("cluster_id unknown", func(t *testing.T) {
		p := newFakePanel(t, failIfCalled(t))
		resp := p.open(context.Background(), t, map[string]tftypes.Value{
			"cluster_id": tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		})
		assertFailedClosed(t, resp, "cluster_id is not known")
	})
}

// The API client has no HTTP timeout and Terraform gives an ephemeral resource no
// deadline, so Open bounds its own call: a hung panel must produce an error, not a hung
// plan.
func TestK8sKubeconfig_OpenTimesOut(t *testing.T) {
	old := openTimeout
	openTimeout = 150 * time.Millisecond
	t.Cleanup(func() { openTimeout = old })

	release := make(chan struct{})
	p := newFakePanel(t, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})

	start := time.Now()
	resp := p.open(context.Background(), t, clusterIDOnly())
	elapsed := time.Since(start)
	close(release)

	assertFailedClosed(t, resp, "Unable to read Kubernetes cluster")
	if elapsed > 10*time.Second {
		t.Errorf("Open took %s against a hung panel, want it bounded by openTimeout", elapsed)
	}
}

// The deadline Open sets on its call must not replace Terraform's own cancellation: when the
// operation is interrupted while the panel hangs, Open ends with it instead of waiting out
// openTimeout.
func TestK8sKubeconfig_OpenHonoursCancellation(t *testing.T) {
	release := make(chan struct{})
	p := newFakePanel(t, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	t.Cleanup(func() { close(release) }) // runs before the server's Close, which waits for the handler

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := kubeconfigConfig(t, clusterIDOnly())
	resp := ephemeral.OpenResponse{Result: tfsdk.EphemeralResultData{Schema: cfg.Schema, Raw: cfg.Raw.Copy()}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&K8sKubeconfigEphemeralResource{c: p.c}).Open(ctx, ephemeral.OpenRequest{Config: cfg}, &resp)
	}()

	// Cancel only once the request is in flight, so it is a running call that gets cut short.
	deadline := time.Now().Add(10 * time.Second)
	for len(p.requests()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("Open never reached the panel")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("Open kept waiting after its context was cancelled (openTimeout is %s)", openTimeout)
	}
	assertFailedClosed(t, resp, "Unable to read Kubernetes cluster")
}

// The default is what a real plan gets: long enough for a slow panel (the client retries
// 429s and transport errors within it), short enough that a hung one cannot stall a plan
// for long. The tests above replace it, so it is checked here.
func TestK8sKubeconfig_OpenTimeoutDefault(t *testing.T) {
	if openTimeout < 30*time.Second || openTimeout > 5*time.Minute {
		t.Errorf("openTimeout = %s, want it between 30s and 5m", openTimeout)
	}
}

func TestHasUsableCredentials(t *testing.T) {
	tests := []struct {
		name string
		kc   client.KubeConfig
		want bool
	}{
		{"certificate and key", client.KubeConfig{ClientCertificate: "c", ClientKey: "k"}, true},
		{"token", client.KubeConfig{Token: "t"}, true},
		{"certificate, key and token", client.KubeConfig{ClientCertificate: "c", ClientKey: "k", Token: "t"}, true},
		// A token is enough by itself, whatever else is half there.
		{"certificate and token", client.KubeConfig{ClientCertificate: "c", Token: "t"}, true},
		{"key and token", client.KubeConfig{ClientKey: "k", Token: "t"}, true},
		{"certificate without key", client.KubeConfig{ClientCertificate: "c"}, false},
		{"key without certificate", client.KubeConfig{ClientKey: "k"}, false},
		{"nothing", client.KubeConfig{}, false},
		{"address and CA only", client.KubeConfig{Host: "h", ClusterCACertificate: "ca"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasUsableCredentials(&tt.kc); got != tt.want {
				t.Errorf("hasUsableCredentials(%+v) = %v, want %v", tt.kc, got, tt.want)
			}
		})
	}
}

// TestK8sKubeconfig_OpenLogsNothingSecret runs Open at TRACE level over the success path
// and several failure paths and checks that neither the resource nor the client under it
// writes a credential, or the API server address, to the provider log.
func TestK8sKubeconfig_OpenLogsNothingSecret(t *testing.T) {
	replies := map[string]struct {
		status int
		body   string
	}{
		"success":       {http.StatusOK, clusterReply(t, "SUCCESS", b64(certKubeconfig()))},
		"token":         {http.StatusOK, clusterReply(t, "SUCCESS", b64(tokenKubeconfig()))},
		"deleted":       {http.StatusOK, clusterReply(t, "DELETED", b64(certKubeconfig()))},
		"no server":     {http.StatusOK, clusterReply(t, "SUCCESS", b64(noServerKubeconfig()))},
		"api failure":   {http.StatusInternalServerError, `{"data":null,"errMessage":"boom","error":500}`},
		"unparsed YAML": {http.StatusOK, clusterReply(t, "SUCCESS", b64(unparsedKubeconfig))},
		"no user":       {http.StatusOK, clusterReply(t, "SUCCESS", b64(userMissingKubeconfig()))},
		"cert files":    {http.StatusOK, clusterReply(t, "SUCCESS", b64(certFilesKubeconfig()))},
	}
	for name, r := range replies {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			ctx := tflogtest.RootLogger(context.Background(), &logs)

			p := newFakePanel(t, replyWith(r.status, r.body))
			p.open(ctx, t, clusterIDOnly())

			assertNoLeak(t, "provider log", logs.String())
			if !strings.Contains(logs.String(), `"cluster_id":313`) {
				t.Errorf("expected the Debug line naming cluster_id 313, log was:\n%s", logs.String())
			}
		})
	}
}
