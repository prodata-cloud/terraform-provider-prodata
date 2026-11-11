package resources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestS3BucketCreateNameConflictNeverAdoptsOrReconfigures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, req.Method+" "+req.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if req.Method == http.MethodPost && req.URL.Path == "/panel-main/storage/api/v1/buckets" {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":626,"message":"Bucket name already exists"}]}`))
			return
		}
		// The old adoption path can read an exact match and even change its ACL.
		// Matching scope/spec does not establish ownership of this Terraform resource.
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{
			"name": "test-bucket", "status": "SUSPENDED", "objectLockEnabled": "",
		}})
	}))
	defer srv.Close()
	c, err := client.New(client.Config{APIBaseURL: srv.URL, APIKeyID: "fake-key", APISecretKey: "fake-secret", Region: "TEST", ProjectTag: "test"})
	if err != nil {
		t.Fatal(err)
	}
	r := &S3BucketResource{c: c}
	var sch resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sch)
	typ := sch.Schema.Type().TerraformType(ctx)
	plan := tfsdk.Plan{Schema: sch.Schema, Raw: tftypes.NewValue(typ, nil)}
	model := S3BucketResourceModel{ID: types.StringUnknown(), Region: types.StringUnknown(), ProjectTag: types.StringUnknown(),
		Name: types.StringValue("test-bucket"), Acl: types.StringValue("private"), Versioning: types.BoolValue(false),
		ObjectLockEnabled: types.BoolValue(false), CreationDate: types.StringUnknown()}
	if diags := plan.Set(ctx, &model); diags.HasError() {
		t.Fatal(diags)
	}
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sch.Schema, Raw: tftypes.NewValue(typ, nil)}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
	if !hasDiag(resp.Diagnostics, diag.SeverityError, "626") || !resp.State.Raw.IsNull() {
		t.Errorf("conflict was not preserved: state=%s diagnostics=%v", resp.State.Raw, resp.Diagnostics)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(calls, []string{"POST /panel-main/storage/api/v1/buckets"}) {
		t.Errorf("name conflict read or modified an existing bucket: %v", calls)
	}
}
