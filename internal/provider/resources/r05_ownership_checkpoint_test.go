package resources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"terraform-provider-prodata/internal/client"
)

func r05Client(t *testing.T, handler http.HandlerFunc) (*client.Client, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	c, err := client.New(client.Config{APIBaseURL: srv.URL, APIKeyID: "fake", APISecretKey: "fake", Region: "TEST", ProjectTag: "project-a"})
	if err != nil {
		t.Fatal(err)
	}
	return c, srv.Close
}

func r05BucketPlan(t *testing.T) (*S3BucketResource, tfsdk.Plan, tfsdk.State) {
	t.Helper()
	ctx := context.Background()
	r := &S3BucketResource{}
	var sch resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sch)
	typ := sch.Schema.Type().TerraformType(ctx)
	plan := tfsdk.Plan{Schema: sch.Schema, Raw: tftypes.NewValue(typ, nil)}
	m := S3BucketResourceModel{ID: types.StringUnknown(), Name: types.StringValue("test-bucket"), Region: types.StringValue("TEST"), ProjectTag: types.StringValue("project-a"), Acl: types.StringValue("private"), Versioning: types.BoolValue(false), ObjectLockEnabled: types.BoolValue(false), CreationDate: types.StringUnknown()}
	if d := plan.Set(ctx, &m); d.HasError() {
		t.Fatal(d)
	}
	return r, plan, tfsdk.State{Schema: sch.Schema, Raw: tftypes.NewValue(typ, nil)}
}

func TestR05Proxy404PreservesLoadBalancerOwnershipOnReadAndDelete(t *testing.T) {
	for _, body := range []string{`<html>Not Found</html>`, `{"status":404,"error":"Not Found"}`, `{"success":false,"errors":[{"code":703,"message":"Other resource missing"}]}`} {
		c, closeServer := r05Client(t, func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(404); _, _ = w.Write([]byte(body)) })
		plan, _ := asgLBPlan(t, 7)
		state := tfsdk.State(plan)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if d := state.SetAttribute(ctx, path.Root("id"), int64(42)); d.HasError() {
			t.Fatal(d)
		}
		_ = state.SetAttribute(ctx, path.Root("source"), client.LbSourceFrontend)
		r := &LbResource{c: c}
		read := resource.ReadResponse{State: state}
		r.Read(ctx, resource.ReadRequest{State: state}, &read)
		if !read.Diagnostics.HasError() || read.State.Raw.IsNull() {
			t.Errorf("proxy404 removed LB: %v", read.Diagnostics)
		}
		deleted := resource.DeleteResponse{State: state}
		r.Delete(ctx, resource.DeleteRequest{State: state}, &deleted)
		if !deleted.Diagnostics.HasError() {
			t.Errorf("proxy404 authorized LB delete: %v", deleted.Diagnostics)
		}
		cancel()
		closeServer()
	}
}

func TestR05Proxy404PreservesBucketOwnershipOnReadAndDelete(t *testing.T) {
	for _, body := range []string{`<html>Not Found</html>`, `{"status":404,"error":"Not Found"}`, `{"success":false,"errors":[{"code":736,"message":"LB missing"}]}`} {
		r, plan, _ := r05BucketPlan(t)
		state := tfsdk.State(plan)
		ctx := context.Background()
		_ = state.SetAttribute(ctx, path.Root("id"), "test-bucket")
		c, closeServer := r05Client(t, func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(404); _, _ = w.Write([]byte(body)) })
		r.c = c
		read := resource.ReadResponse{State: state}
		r.Read(ctx, resource.ReadRequest{State: state}, &read)
		if !read.Diagnostics.HasError() || read.State.Raw.IsNull() {
			t.Errorf("proxy404 removed bucket: %v", read.Diagnostics)
		}
		deleted := resource.DeleteResponse{State: state}
		r.Delete(ctx, resource.DeleteRequest{State: state}, &deleted)
		if !deleted.Diagnostics.HasError() {
			t.Errorf("proxy404 authorized bucket delete: %v", deleted.Diagnostics)
		}
		closeServer()
	}
}

func TestR05BucketCreateCheckpointsAcknowledgedOwnershipBeforeAnyRead(t *testing.T) {
	for _, failPath := range []string{"/panel-main/storage/api/v1/buckets/test-bucket", "/panel-main/storage/api/v1/buckets/test-bucket/versioning"} {
		r, plan, empty := r05BucketPlan(t)
		c, closeServer := r05Client(t, func(w http.ResponseWriter, req *http.Request) {
			if req.Method == http.MethodPost {
				w.WriteHeader(201)
				return
			}
			if req.URL.Path == failPath {
				w.WriteHeader(500)
				_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":627,"message":"unavailable"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"data":{"name":"test-bucket"}}`))
		})
		r.c = c
		ctx := context.Background()
		resp := resource.CreateResponse{State: empty}
		r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("read failure was not surfaced")
		}
		var id types.String
		d := resp.State.GetAttribute(ctx, path.Root("id"), &id)
		if d.HasError() || id.ValueString() != "test-bucket" || !resp.State.Raw.IsFullyKnown() {
			t.Errorf("acknowledged bucket became unowned: id=%v state=%v diagnostics=%v", id, resp.State.Raw, resp.Diagnostics)
		}
		closeServer()
	}
}

func TestR05ExplicitResourceAbsenceStillCompletesReadAndDelete(t *testing.T) {
	ctx := context.Background()
	c, closeServer := r05Client(t, func(w http.ResponseWriter, req *http.Request) {
		code := 736
		if strings.Contains(req.URL.Path, "/buckets") {
			code = 628
		}
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]any{{"code": code, "message": "Missing"}}})
	})
	defer closeServer()
	plan, lbState := r05LBState(t, client.LbSourceFrontend)
	_ = plan
	lb := &LbResource{c: c}
	lbRead := resource.ReadResponse{State: lbState}
	lb.Read(ctx, resource.ReadRequest{State: lbState}, &lbRead)
	if lbRead.Diagnostics.HasError() || !lbRead.State.Raw.IsNull() {
		t.Fatalf("explicit LB736 not removed: %v", lbRead.Diagnostics)
	}
	lbDelete := resource.DeleteResponse{State: lbState}
	lb.Delete(ctx, resource.DeleteRequest{State: lbState}, &lbDelete)
	if lbDelete.Diagnostics.HasError() {
		t.Fatal(lbDelete.Diagnostics)
	}
	bucket, bucketPlan, _ := r05BucketPlan(t)
	bucket.c = c
	bucketState := tfsdk.State(bucketPlan)
	_ = bucketState.SetAttribute(ctx, path.Root("id"), "test-bucket")
	bucketRead := resource.ReadResponse{State: bucketState}
	bucket.Read(ctx, resource.ReadRequest{State: bucketState}, &bucketRead)
	if bucketRead.Diagnostics.HasError() || !bucketRead.State.Raw.IsNull() {
		t.Fatalf("explicit bucket628 not removed: %v", bucketRead.Diagnostics)
	}
	bucketDelete := resource.DeleteResponse{State: bucketState}
	bucket.Delete(ctx, resource.DeleteRequest{State: bucketState}, &bucketDelete)
	if bucketDelete.Diagnostics.HasError() {
		t.Fatal(bucketDelete.Diagnostics)
	}
}

func TestR05FailedLBUpdateRestoresObservedStateFromPlanSeed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, closeServer := r05Client(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":627,"message":"Read unavailable"}]}`))
	})
	defer closeServer()
	_, state := r05LBState(t, client.LbSourceFrontend)
	plan, _ := asgLBPlan(t, 8)
	_ = plan.SetAttribute(ctx, path.Root("id"), int64(42))
	_ = plan.SetAttribute(ctx, path.Root("source"), client.LbSourceFrontend)
	resp := resource.UpdateResponse{State: tfsdk.State(plan)}
	(&LbResource{c: c}).Update(ctx, resource.UpdateRequest{State: state, Plan: plan}, &resp)
	var asg types.Int64
	d := resp.State.GetAttribute(ctx, path.Root("backend_group").AtName("asg_id"), &asg)
	if !resp.Diagnostics.HasError() || d.HasError() || asg.ValueInt64() != 7 {
		t.Fatalf("failed update claimed desired selector: asg=%v d=%v diagnostic=%v", asg, d, resp.Diagnostics)
	}
}
