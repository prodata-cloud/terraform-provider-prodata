package datasources

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Tests for exclude_credentials_from_state on the cluster data source: with it on,
// kube_config and private_key_encoded are null in the state the data source writes, and
// nothing else changes.

func credDSB64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// Distinct markers: if one turns up in the state, a credential got there.
var (
	credDSMarkerCA         = credDSB64("FAKE-CA-PEM")
	credDSMarkerCert       = credDSB64("FAKE-CLIENT-CERT-PEM")
	credDSMarkerKey        = credDSB64("FAKE-CLIENT-KEY-PEM")
	credDSMarkerPrivateKey = "FAKE-SSH-PRIVATE-KEY-MARKER"
)

const credDSPublicKey = "ssh-rsa AAAAFAKEPUBLICKEY node@cluster"

func credDSKubeconfigYAML() string {
	return `apiVersion: v1
kind: Config
current-context: admin@tfc
clusters:
- name: tfc
  cluster:
    server: https://k8s.example.com:6443
    certificate-authority-data: ` + credDSMarkerCA + `
contexts:
- name: admin@tfc
  context:
    cluster: tfc
    user: admin
users:
- name: admin
  user:
    client-certificate-data: ` + credDSMarkerCert + `
    client-key-data: ` + credDSMarkerKey + `
`
}

func credDSClusterBody() map[string]any {
	return map[string]any{
		"id": 313, "name": "tfc", "status": map[string]any{"name": "SUCCESS"},
		"kuberVersion": "v1.31.4", "apiEndpoint": "https://k8s.example.com:6443",
		"isPublic": false, "isHa": false, "blocked": false,
		"sshKeyEncoded": credDSPublicKey, "privateKeyEncoded": credDSMarkerPrivateKey,
		"clusterConfigSecret": credDSB64(credDSKubeconfigYAML()),
		"masterNodeCount":     1, "workerNodeCount": 0, "nodePoolCount": 0,
		"masterNodeConfiguration": map[string]any{"id": 7, "cpu": 2, "ram": 4, "ssd": 20, "isHa": false},
		"podSubnet":               "10.244.0.0/16", "nodeIpRange": "10.0.1.10-10.0.1.20",
		"ipAddressesCount": 5, "projectId": 89, "dateCreated": "2026-10-01T00:00:00Z",
	}
}

// credDSPanel is a minimal panel-main with one cluster, id 313, found by id or by name.
func credDSPanel(t *testing.T) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const base = "/panel-main/api/kubernetes"
		var data any
		switch {
		case r.Method == http.MethodGet && r.URL.Path == base+"/getCluster/313":
			data = credDSClusterBody()
		case r.Method == http.MethodGet && r.URL.Path == base+"/getClusters":
			data = []any{credDSClusterBody()}
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
	return c
}

func credDSConfig(t *testing.T, set map[string]tftypes.Value) tfsdk.Config {
	t.Helper()
	var sr datasource.SchemaResponse
	NewK8sClusterDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &sr)
	if sr.Diagnostics.HasError() {
		t.Fatalf("schema: %v", sr.Diagnostics)
	}
	obj, ok := sr.Schema.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("schema type is not an object")
	}
	vals := make(map[string]tftypes.Value, len(obj.AttributeTypes))
	for k, typ := range obj.AttributeTypes {
		vals[k] = tftypes.NewValue(typ, nil)
	}
	for k, v := range set {
		if _, known := vals[k]; !known {
			t.Fatalf("config sets %q, which is not a schema attribute", k)
		}
		vals[k] = v
	}
	return tfsdk.Config{Schema: sr.Schema, Raw: tftypes.NewValue(obj, vals)}
}

func credDSFlag(name string) tftypes.Value {
	switch name {
	case "true":
		return tftypes.NewValue(tftypes.Bool, true)
	case "false":
		return tftypes.NewValue(tftypes.Bool, false)
	}
	return tftypes.NewValue(tftypes.Bool, nil)
}

func TestClusterDataSource_ExcludeCredentialsFromState(t *testing.T) {
	lookups := map[string]map[string]tftypes.Value{
		"by id":   {"id": tftypes.NewValue(tftypes.Number, 313)},
		"by name": {"name": tftypes.NewValue(tftypes.String, "tfc")},
	}
	for lookup, set := range lookups {
		for _, flag := range []string{"null", "false", "true"} {
			t.Run(lookup+", flag "+flag, func(t *testing.T) {
				cfgSet := map[string]tftypes.Value{"exclude_credentials_from_state": credDSFlag(flag)}
				for k, v := range set {
					cfgSet[k] = v
				}
				cfg := credDSConfig(t, cfgSet)
				resp := datasource.ReadResponse{State: tfsdk.State{Schema: cfg.Schema, Raw: tftypes.NewValue(cfg.Raw.Type(), nil)}}
				(&K8sClusterDataSource{c: credDSPanel(t)}).Read(context.Background(), datasource.ReadRequest{Config: cfg}, &resp)

				if resp.Diagnostics.HasError() {
					t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
				}
				var got K8sClusterDataSourceModel
				if d := resp.State.Get(context.Background(), &got); d.HasError() {
					t.Fatalf("read state: %v", d)
				}

				if flag == "true" {
					if !got.KubeConfig.IsNull() || !got.PrivateKeyEncoded.IsNull() {
						t.Errorf("kube_config / private_key_encoded must be null: %v / %v", got.KubeConfig, got.PrivateKeyEncoded)
					}
					state := resp.State.Raw.String()
					for _, m := range []string{
						credDSMarkerCA, credDSMarkerCert, credDSMarkerKey, credDSMarkerPrivateKey,
						"FAKE-CA-PEM", "FAKE-CLIENT-CERT-PEM", "FAKE-CLIENT-KEY-PEM",
					} {
						if strings.Contains(state, m) {
							t.Errorf("state contains %q", m)
						}
					}
				} else {
					if got.KubeConfig.IsNull() {
						t.Error("kube_config must be populated")
					} else if host, _ := got.KubeConfig.Attributes()["host"].(types.String); host.ValueString() != "https://k8s.example.com:6443" {
						t.Errorf("kube_config.host = %v", host)
					}
					if got.PrivateKeyEncoded.ValueString() != credDSMarkerPrivateKey {
						t.Errorf("private_key_encoded = %v, want the key", got.PrivateKeyEncoded)
					}
				}

				// The flag is about the credentials only; the rest of the cluster is read as usual,
				// including the public half of the SSH key.
				if got.SSHKeyEncoded.ValueString() != credDSPublicKey || got.Status.ValueString() != "SUCCESS" ||
					got.APIEndpoint.ValueString() != "https://k8s.example.com:6443" || got.ID.ValueInt64() != 313 ||
					got.Name.ValueString() != "tfc" {
					t.Errorf("non-credential fields must be unaffected: %+v", got)
				}
				// The flag itself is echoed back as configured.
				wantFlag := map[string]types.Bool{
					"null": types.BoolNull(), "false": types.BoolValue(false), "true": types.BoolValue(true),
				}[flag]
				if !got.ExcludeCredentialsFromState.Equal(wantFlag) {
					t.Errorf("exclude_credentials_from_state = %v, want %v", got.ExcludeCredentialsFromState, wantFlag)
				}
			})
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
