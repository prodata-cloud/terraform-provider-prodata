package resources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestVolumeResource_CreateChecksCapabilityBeforeAllocation(t *testing.T) {
	var creates atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			creates.Add(1)
		}
		http.NotFound(w, req)
	}))
	defer srv.Close()
	c, err := client.New(client.Config{APIBaseURL: srv.URL, APIKeyID: "fake-key", APISecretKey: "fake-secret", Region: "TEST", ProjectTag: "test"})
	if err != nil {
		t.Fatal(err)
	}
	r := &VolumeResource{client: c}
	ctx := context.Background()
	var sch resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sch)
	typ := sch.Schema.Type().TerraformType(ctx)
	plan := tfsdk.Plan{Schema: sch.Schema, Raw: tftypes.NewValue(typ, nil)}
	model := VolumeResourceModel{ID: types.Int64Unknown(), Region: types.StringUnknown(), ProjectTag: types.StringUnknown(), Name: types.StringValue("data"), Type: types.StringValue("SSD"), Size: types.Int64Value(10)}
	if diags := plan.Set(ctx, &model); diags.HasError() {
		t.Fatal(diags)
	}
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sch.Schema, Raw: tftypes.NewValue(typ, nil)}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
	if !resp.Diagnostics.HasError() || creates.Load() != 0 || !resp.State.Raw.IsNull() {
		t.Fatalf("old server caused allocation: creates=%d diagnostics=%v", creates.Load(), resp.Diagnostics)
	}
}

func TestVolumeResource_ReadRequiresConfirmedAbsence(t *testing.T) {
	for _, tc := range []struct {
		name, data          string
		status              int
		wantError, wantGone bool
	}{
		{"attached with a different attachment ID", `{"identityVersion":"user-disk-v1","id":9,"exists":true,"name":"data","size":10,"type":"SSD","inUse":true,"attachedId":77,"attachmentId":515}`, 200, false, false},
		{"confirmed absent", `{"identityVersion":"user-disk-v1","id":9,"exists":false}`, 200, false, true},
		{"unsupported old API", `null`, 404, true, false},
		{"API failure", `null`, 500, true, false},
		{"legacy success cannot prove absence", `{"id":9}`, 200, true, false},
		{"different disk cannot prove absence", `{"identityVersion":"user-disk-v1","id":515,"exists":false}`, 200, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodGet || req.URL.Path != "/panel-main/api/v2/volumes/by-user-disk/9" ||
					req.Header.Get("X-Region") != "OTHER" || req.Header.Get("X-Project-Tag") != "stored-project" {
					t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"success":true,"data":` + tc.data + `}`))
			}))
			defer srv.Close()
			c, err := client.New(client.Config{APIBaseURL: srv.URL, APIKeyID: "fake-key", APISecretKey: "fake-secret", Region: "TEST", ProjectTag: "changed-provider-project"})
			if err != nil {
				t.Fatal(err)
			}
			r := &VolumeResource{client: c}
			var sch resource.SchemaResponse
			r.Schema(ctx, resource.SchemaRequest{}, &sch)
			state := tfsdk.State{Schema: sch.Schema, Raw: tftypes.NewValue(sch.Schema.Type().TerraformType(ctx), nil)}
			model := VolumeResourceModel{Status: types.StringValue("AVAILABLE"), ID: types.Int64Value(9), Region: types.StringValue("OTHER"), ProjectTag: types.StringValue("stored-project"), Name: types.StringValue("data"), Type: types.StringValue("SSD"), Size: types.Int64Value(10)}
			if diags := state.Set(ctx, &model); diags.HasError() {
				t.Fatal(diags)
			}
			resp := resource.ReadResponse{State: state}
			r.Read(ctx, resource.ReadRequest{State: state}, &resp)
			if resp.Diagnostics.HasError() != tc.wantError || resp.State.Raw.IsNull() != tc.wantGone {
				t.Fatalf("diagnostics=%v state=%s", resp.Diagnostics, resp.State.Raw)
			}
			if !tc.wantGone && !resp.State.Raw.Equal(state.Raw) {
				t.Error("refresh changed the persistent identity or scope")
			}
		})
	}
}

func TestVolumeResource_DeleteDoesNotTreatMissingRouteAsDeleted(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	c, err := client.New(client.Config{APIBaseURL: srv.URL, APIKeyID: "fake-key", APISecretKey: "fake-secret"})
	if err != nil {
		t.Fatal(err)
	}
	r := &VolumeResource{client: c}
	ctx := context.Background()
	var sch resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sch)
	state := tfsdk.State{Schema: sch.Schema, Raw: tftypes.NewValue(sch.Schema.Type().TerraformType(ctx), nil)}
	if diags := state.Set(ctx, &VolumeResourceModel{ID: types.Int64Value(9)}); diags.HasError() {
		t.Fatal(diags)
	}
	resp := resource.DeleteResponse{State: state}
	r.Delete(ctx, resource.DeleteRequest{State: state}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("an old server's route404 was accepted as successful deletion")
	}
}

func TestVolumeAttachmentResource_RequiresVerifiedVmAbsence(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		gone       bool
	}{
		{"old route404", `{}`, 404, false},
		{"uncertain snapshot", `{"success":false,"errors":[{"code":627,"message":"unverified attachment"}]}`, 500, false},
		{"scope denied", `{"success":false,"errors":[{"code":714,"message":"denied"}]}`, 403, false},
		{"confirmed VM absent", `{"success":false,"errors":[{"code":601,"message":"VM not found"}]}`, 404, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c, err := client.New(client.Config{APIBaseURL: srv.URL, APIKeyID: "fake-key", APISecretKey: "fake-secret"})
			if err != nil {
				t.Fatal(err)
			}
			r := &VolumeAttachmentResource{client: c}
			ctx := context.Background()
			var sch resource.SchemaResponse
			r.Schema(ctx, resource.SchemaRequest{}, &sch)
			state := tfsdk.State{Schema: sch.Schema, Raw: tftypes.NewValue(sch.Schema.Type().TerraformType(ctx), nil)}
			model := VolumeAttachmentResourceModel{VmID: types.Int64Value(77), VolumeID: types.Int64Value(9), AttachedVolumeID: types.Int64Value(515), Region: types.StringValue("TEST"), ProjectTag: types.StringValue("test")}
			if diags := state.Set(ctx, &model); diags.HasError() {
				t.Fatal(diags)
			}
			resp := resource.ReadResponse{State: state}
			r.Read(ctx, resource.ReadRequest{State: state}, &resp)
			if resp.State.Raw.IsNull() != tc.gone || resp.Diagnostics.HasError() == tc.gone {
				t.Fatalf("state=%v diagnostics=%v", resp.State.Raw, resp.Diagnostics)
			}
		})
	}
}

func TestVolumeAttachmentResource_DeleteRequiresVerifiedVmAbsence(t *testing.T) {
	for _, tc := range []struct {
		name, vmStatus string
		failStatus     bool
		status         int
		body           string
		wantError      bool
	}{
		{"status route404", "", true, 404, `{}`, true},
		{"confirmed deleted VM", "", true, 404, `{"success":false,"errors":[{"code":601,"message":"VM not found"}]}`, false},
		{"stopped detach route404", "STOPPED", false, 404, `{}`, true},
		{"error detach route404", "ERROR", false, 404, `{}`, true},
		{"confirmed VM absent during detach", "STOPPED", false, 404, `{"success":false,"errors":[{"code":601,"message":"VM not found"}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/volume-attachments/capabilities") && !tc.failStatus {
					protectionReply(w, map[string]any{"contract": client.VolumeAttachmentContract, "reserveBeforeSubmit": true, "recoverableSubmission": true})
					return
				}
				if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/volume-attachments/515") && !tc.failStatus {
					protectionReply(w, attachmentView("SUCCESS", "ATTACH", true))
					return
				}
				if r.Method == http.MethodGet && !tc.failStatus {
					_, _ = w.Write([]byte(`{"success":true,"data":{"id":77,"status":"` + tc.vmStatus + `"}}`))
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c, err := client.New(client.Config{APIBaseURL: srv.URL, APIKeyID: "fake-key", APISecretKey: "fake-secret"})
			if err != nil {
				t.Fatal(err)
			}
			r := &VolumeAttachmentResource{client: c}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var sch resource.SchemaResponse
			r.Schema(ctx, resource.SchemaRequest{}, &sch)
			state := tfsdk.State{Schema: sch.Schema, Raw: tftypes.NewValue(sch.Schema.Type().TerraformType(ctx), nil)}
			model := VolumeAttachmentResourceModel{VmID: types.Int64Value(77), VolumeID: types.Int64Value(9), AttachedVolumeID: types.Int64Value(515), Region: types.StringValue("TEST"), ProjectTag: types.StringValue("test")}
			if d := state.Set(ctx, &model); d.HasError() {
				t.Fatal(d)
			}
			resp := resource.DeleteResponse{State: state}
			r.Delete(ctx, resource.DeleteRequest{State: state}, &resp)
			if resp.Diagnostics.HasError() != tc.wantError || !resp.State.Raw.Equal(state.Raw) {
				t.Fatalf("diagnostics=%v state=%v", resp.Diagnostics, resp.State.Raw)
			}
		})
	}
}
