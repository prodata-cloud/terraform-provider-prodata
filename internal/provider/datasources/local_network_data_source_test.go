package datasources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

const localNetworksListJSON = `{"success":true,"data":[` +
	`{"id":1,"name":"alpha","cidr":"10.0.1.0/24","gateway":"10.0.1.1","linked":false},` +
	`{"id":2,"name":"Beta","cidr":"10.0.2.0/24","gateway":"10.0.2.1","linked":true},` +
	`{"id":3,"name":"beta","cidr":"10.0.3.0/24","gateway":"10.0.3.1","linked":false},` +
	`{"id":4,"name":"dup","cidr":"10.0.4.0/24","gateway":"10.0.4.1","linked":false},` +
	`{"id":5,"name":"dup","cidr":"10.0.5.0/24","gateway":"10.0.5.1","linked":false}` +
	`]}`

func TestMatchLocalNetworksByName(t *testing.T) {
	networks := []client.LocalNetwork{
		{ID: 1, Name: "alpha"},
		{ID: 2, Name: "Beta"},
		{ID: 3, Name: "beta"},
		{ID: 4, Name: "dup"},
		{ID: 5, Name: "dup"},
	}

	tests := []struct {
		name    string
		lookup  string
		wantIDs []int64
	}{
		{"single match", "alpha", []int64{1}},
		{"match is case-sensitive: Beta", "Beta", []int64{2}},
		{"match is case-sensitive: beta", "beta", []int64{3}},
		{"no case folding", "ALPHA", nil},
		{"no substring match", "alph", nil},
		{"no match", "missing", nil},
		{"duplicates are all returned", "dup", []int64{4, 5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchLocalNetworksByName(networks, tt.lookup)
			if len(got) != len(tt.wantIDs) {
				t.Fatalf("got %d matches, want %d: %+v", len(got), len(tt.wantIDs), got)
			}
			for i, id := range tt.wantIDs {
				if got[i].ID != id {
					t.Errorf("match %d: got id %d, want %d", i, got[i].ID, id)
				}
			}
		})
	}

	if got := matchLocalNetworksByName(nil, "alpha"); len(got) != 0 {
		t.Errorf("nil list: got %d matches, want 0", len(got))
	}
}

func localNetworkSchema(t *testing.T) dschema.Schema {
	t.Helper()
	var resp datasource.SchemaResponse
	NewLocalNetworkDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func TestLocalNetworkDataSource_SchemaMatchesModel(t *testing.T) {
	s := localNetworkSchema(t)
	assertTagsMatch(t, "local_network", reflect.TypeOf(LocalNetworkDataSourceModel{}), s.Attributes)
}

func TestLocalNetworkDataSource_SchemaLookupKeys(t *testing.T) {
	s := localNetworkSchema(t)

	id, ok := s.Attributes["id"].(dschema.Int64Attribute)
	if !ok || !id.Optional || !id.Computed || id.Required {
		t.Errorf("id must be Optional+Computed (not Required): %+v", s.Attributes["id"])
	}
	name, ok := s.Attributes["name"].(dschema.StringAttribute)
	if !ok || !name.Optional || !name.Computed || name.Required {
		t.Errorf("name must be Optional+Computed (not Required): %+v", s.Attributes["name"])
	}

	// An empty name would silently match nothing; reject it at validate time.
	var resp validator.StringResponse
	for _, v := range name.Validators {
		v.ValidateString(context.Background(), validator.StringRequest{ConfigValue: types.StringValue("")}, &resp)
	}
	if !resp.Diagnostics.HasError() {
		t.Error("empty name must be rejected by a validator")
	}
}

// localNetworkConfig builds a tfsdk.Config in which every attribute is null except the
// ones passed in.
func localNetworkConfig(t *testing.T, set map[string]tftypes.Value) tfsdk.Config {
	t.Helper()
	s := localNetworkSchema(t)
	objType, ok := s.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("schema type is not an object")
	}
	vals := make(map[string]tftypes.Value, len(objType.AttributeTypes))
	for k, typ := range objType.AttributeTypes {
		vals[k] = tftypes.NewValue(typ, nil)
	}
	for k, v := range set {
		vals[k] = v
	}
	return tfsdk.Config{Schema: s, Raw: tftypes.NewValue(objType, vals)}
}

func TestLocalNetworkDataSource_ExactlyOneOfIDOrName(t *testing.T) {
	ds := NewLocalNetworkDataSource().(*LocalNetworkDataSource)

	tests := []struct {
		name    string
		set     map[string]tftypes.Value
		wantErr bool
	}{
		{"neither", nil, true},
		{"id only", map[string]tftypes.Value{"id": tftypes.NewValue(tftypes.Number, 7)}, false},
		{"name only", map[string]tftypes.Value{"name": tftypes.NewValue(tftypes.String, "alpha")}, false},
		{"both", map[string]tftypes.Value{
			"id":   tftypes.NewValue(tftypes.Number, 7),
			"name": tftypes.NewValue(tftypes.String, "alpha"),
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := localNetworkConfig(t, tt.set)
			var resp datasource.ValidateConfigResponse
			for _, v := range ds.ConfigValidators(context.Background()) {
				v.ValidateDataSource(context.Background(), datasource.ValidateConfigRequest{Config: cfg}, &resp)
			}
			if got := resp.Diagnostics.HasError(); got != tt.wantErr {
				t.Errorf("HasError = %v, want %v (%v)", got, tt.wantErr, resp.Diagnostics)
			}
		})
	}
}

// readLocalNetwork runs the data source Read against a fake panel and returns the response
// plus the requests the fake received.
func readLocalNetwork(t *testing.T, set map[string]tftypes.Value, handler http.HandlerFunc) (datasource.ReadResponse, []*http.Request) {
	t.Helper()

	var seen []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r)
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

	ds := &LocalNetworkDataSource{client: c}
	cfg := localNetworkConfig(t, set)
	resp := datasource.ReadResponse{
		State: tfsdk.State{Schema: cfg.Schema, Raw: tftypes.NewValue(cfg.Raw.Type(), nil)},
	}
	ds.Read(context.Background(), datasource.ReadRequest{Config: cfg}, &resp)
	return resp, seen
}

func jsonHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func TestLocalNetworkDataSource_ReadByName(t *testing.T) {
	resp, seen := readLocalNetwork(t,
		map[string]tftypes.Value{"name": tftypes.NewValue(tftypes.String, "Beta")},
		jsonHandler(localNetworksListJSON))

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	if len(seen) != 1 || seen[0].URL.Path != "/panel-main/api/v2/local-networks" {
		t.Fatalf("expected one call to the list endpoint, got %d: %+v", len(seen), seen)
	}
	if got := seen[0].Header.Get("X-Region"); got != "TEST" {
		t.Errorf("X-Region = %q, want TEST", got)
	}
	if got := seen[0].Header.Get("X-Project-Tag"); got != "test-project" {
		t.Errorf("X-Project-Tag = %q, want test-project", got)
	}

	var got LocalNetworkDataSourceModel
	if diags := resp.State.Get(context.Background(), &got); diags.HasError() {
		t.Fatalf("read state: %v", diags)
	}
	if got.ID.ValueInt64() != 2 || got.Name.ValueString() != "Beta" ||
		got.CIDR.ValueString() != "10.0.2.0/24" || got.Gateway.ValueString() != "10.0.2.1" || !got.Linked.ValueBool() {
		t.Errorf("unexpected state: %+v", got)
	}
}

func TestLocalNetworkDataSource_ReadByNameErrors(t *testing.T) {
	tests := []struct {
		name    string
		lookup  string
		handler http.HandlerFunc
		wantMsg string
	}{
		{"not found", "gamma", jsonHandler(localNetworksListJSON), "No local network named"},
		{"case differs", "ALPHA", jsonHandler(localNetworksListJSON), "No local network named"},
		{"ambiguous", "dup", jsonHandler(localNetworksListJSON), "look the network up by id"},
		{"empty list", "alpha", jsonHandler(`{"success":true,"data":[]}`), "No local network named"},
		{"list fails", "alpha", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":627,"message":"boom"}]}`))
		}, "Unable to List Local Networks"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, _ := readLocalNetwork(t,
				map[string]tftypes.Value{"name": tftypes.NewValue(tftypes.String, tt.lookup)},
				tt.handler)

			if !resp.Diagnostics.HasError() {
				t.Fatal("expected an error diagnostic")
			}
			var all strings.Builder
			for _, d := range resp.Diagnostics {
				all.WriteString(d.Summary() + " " + d.Detail() + "\n")
			}
			if !strings.Contains(all.String(), tt.wantMsg) {
				t.Errorf("diagnostics %q do not contain %q", all.String(), tt.wantMsg)
			}
		})
	}
}

func TestLocalNetworkDataSource_ReadByID(t *testing.T) {
	resp, seen := readLocalNetwork(t,
		map[string]tftypes.Value{"id": tftypes.NewValue(tftypes.Number, 3)},
		jsonHandler(`{"success":true,"data":{"id":3,"name":"beta","cidr":"10.0.3.0/24","gateway":"10.0.3.1","linked":false}}`))

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	if len(seen) != 1 || seen[0].URL.Path != "/panel-main/api/v2/local-networks/3" {
		t.Fatalf("expected one call to the by-id endpoint, got %d: %+v", len(seen), seen)
	}

	var got LocalNetworkDataSourceModel
	if diags := resp.State.Get(context.Background(), &got); diags.HasError() {
		t.Fatalf("read state: %v", diags)
	}
	if got.ID.ValueInt64() != 3 || got.Name.ValueString() != "beta" || got.CIDR.ValueString() != "10.0.3.0/24" {
		t.Errorf("unexpected state: %+v", got)
	}
}

func TestLocalNetworkDataSource_ReadByNameHonorsScopeOverride(t *testing.T) {
	resp, seen := readLocalNetwork(t,
		map[string]tftypes.Value{
			"name":        tftypes.NewValue(tftypes.String, "alpha"),
			"region":      tftypes.NewValue(tftypes.String, "OTHER"),
			"project_tag": tftypes.NewValue(tftypes.String, "p2"),
		},
		jsonHandler(localNetworksListJSON))

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	if len(seen) != 1 {
		t.Fatalf("expected one call, got %d", len(seen))
	}
	if got := seen[0].Header.Get("X-Region"); got != "OTHER" {
		t.Errorf("X-Region = %q, want OTHER (the region attribute must override the provider default)", got)
	}
	if got := seen[0].Header.Get("X-Project-Tag"); got != "p2" {
		t.Errorf("X-Project-Tag = %q, want p2 (the project_tag attribute must override the provider default)", got)
	}
}

func TestLocalNetworkDataSource_ReadByIDHonorsScopeOverride(t *testing.T) {
	resp, seen := readLocalNetwork(t,
		map[string]tftypes.Value{
			"id":          tftypes.NewValue(tftypes.Number, 3),
			"region":      tftypes.NewValue(tftypes.String, "OTHER"),
			"project_tag": tftypes.NewValue(tftypes.String, "p2"),
		},
		jsonHandler(`{"success":true,"data":{"id":3,"name":"beta","cidr":"10.0.3.0/24","gateway":"10.0.3.1","linked":false}}`))

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	if len(seen) != 1 {
		t.Fatalf("expected one call, got %d", len(seen))
	}
	if got := seen[0].Header.Get("X-Region"); got != "OTHER" {
		t.Errorf("X-Region = %q, want OTHER", got)
	}
	if got := seen[0].Header.Get("X-Project-Tag"); got != "p2" {
		t.Errorf("X-Project-Tag = %q, want p2", got)
	}
	if got := seen[0].URL.Query().Get("region"); got != "OTHER" {
		t.Errorf("query region = %q, want OTHER", got)
	}
	if got := seen[0].URL.Query().Get("projectTag"); got != "p2" {
		t.Errorf("query projectTag = %q, want p2", got)
	}
}

// ExactlyOneOf passes while either key is unknown at validate time, so Read must refuse a
// config where both are set rather than silently let id win and ignore name.
func TestLocalNetworkDataSource_ReadRejectsBothIDAndName(t *testing.T) {
	resp, seen := readLocalNetwork(t,
		map[string]tftypes.Value{
			"id":   tftypes.NewValue(tftypes.Number, 3),
			"name": tftypes.NewValue(tftypes.String, "alpha"),
		},
		jsonHandler(localNetworksListJSON))

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error diagnostic when both id and name are set")
	}
	if len(seen) != 0 {
		t.Errorf("no API call may be made for a conflicting config, got %d", len(seen))
	}
}
