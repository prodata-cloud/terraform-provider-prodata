package resources

import (
	"context"
	"encoding/json"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"net/http"
	"net/http/httptest"
	"strings"
	"terraform-provider-prodata/internal/client"
	"testing"
	"time"
)

func volumeCreationFixture(t *testing.T, url string) (*VolumeResource, resource.CreateRequest, *resource.CreateResponse) {
	t.Helper()
	c, err := client.New(client.Config{APIBaseURL: url, APIKeyID: "fixture", APISecretKey: "fixture", Region: "TEST", ProjectTag: "test"})
	if err != nil {
		t.Fatal(err)
	}
	r := &VolumeResource{client: c, poll: time.Millisecond}
	ctx := context.Background()
	var s resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &s)
	plan := tfsdk.Plan{Schema: s.Schema, Raw: tftypes.NewValue(s.Schema.Type().TerraformType(ctx), nil)}
	m := VolumeResourceModel{ID: types.Int64Unknown(), RequestUUID: types.StringUnknown(), Status: types.StringUnknown(), Region: types.StringUnknown(), ProjectTag: types.StringUnknown(), Name: types.StringValue("data"), Type: types.StringValue("SSD"), Size: types.Int64Value(10)}
	if d := plan.Set(ctx, &m); d.HasError() {
		t.Fatal(d)
	}
	return r, resource.CreateRequest{Plan: plan}, &resource.CreateResponse{State: tfsdk.State{Schema: s.Schema, Raw: tftypes.NewValue(s.Schema.Type().TerraformType(ctx), nil)}}
}
func creationView(uuid, status string, id any) map[string]any {
	v := map[string]any{"contract": client.VolumeCreationContract, "requestUuid": uuid, "volumeId": id, "region": "TEST", "projectTag": "test", "name": "data", "type": "SSD", "size": 10, "status": status, "exists": true, "canRelease": false}
	if status == "CANCELLED" {
		v["exists"], v["canRelease"] = false, true
	}
	if status == "UNKNOWN" {
		v["exists"], v["volumeId"] = nil, nil
	}
	return v
}
func lostVolumeAck(w http.ResponseWriter) {
	h, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "fixture does not support connection loss", http.StatusInternalServerError)
		return
	}
	connection, _, err := h.Hijack()
	if err == nil {
		_ = connection.Close()
	}
}

func TestVolumeCreationCheckpointAndLostReserveAndSubmitAck(t *testing.T) {
	var resp *resource.CreateResponse
	requestUUID, state := "", ""
	reserves, submits, physical := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		reply := func(v any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": v})
		}
		if strings.HasSuffix(q.URL.Path, "/creation/capabilities") {
			var checkpoint VolumeResourceModel
			if d := resp.State.Get(context.Background(), &checkpoint); d.HasError() {
				t.Error(d)
			}
			if checkpoint.RequestUUID.IsNull() || checkpoint.RequestUUID.IsUnknown() || !checkpoint.ID.IsNull() {
				t.Errorf("identity not checkpointed before firstHTTP: %+v", checkpoint)
			}
			reply(map[string]any{"contract": client.VolumeCreationContract, "reserveBeforeSubmit": true, "requestLookup": true, "cancelTombstone": true, "canonicalIdentity": "user-disk-v1"})
			return
		}
		if q.Header.Get("X-Region") != "TEST" || q.Header.Get("X-Project-Tag") != "test" {
			t.Error("scope was not retained")
		}
		base := "/panel-main/api/v2/volumes/by-user-disk/creation/"
		id := strings.TrimSuffix(strings.TrimPrefix(q.URL.Path, base), "/submit")
		if requestUUID != "" && id != requestUUID {
			t.Error("request UUID changed after uncertain outcome")
		}
		requestUUID = id
		if q.Method == http.MethodPost && strings.HasSuffix(q.URL.Path, "/submit") {
			submits++
			physical++
			state = "READY"
			lostVolumeAck(w)
			return
		}
		if q.Method == http.MethodPost {
			reserves++
			state = "RESERVED"
			lostVolumeAck(w)
			return
		}
		if q.Method == http.MethodGet {
			reply(creationView(requestUUID, state, 9))
			return
		}
		t.Errorf("unexpected %s %s", q.Method, q.URL.Path)
	}))
	defer server.Close()
	r, req, output := volumeCreationFixture(t, server.URL)
	resp = output
	r.Create(context.Background(), req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var got VolumeResourceModel
	if d := resp.State.Get(context.Background(), &got); d.HasError() {
		t.Fatal(d)
	}
	if got.ID.ValueInt64() != 9 || got.RequestUUID.ValueString() != requestUUID || got.Status.ValueString() != "READY" || reserves != 1 || submits != 1 || physical != 1 {
		t.Fatalf("unsafe recovery model=%+v reserve/submit/physical=%d/%d/%d", got, reserves, submits, physical)
	}
}

func TestVolumeCreationUnknownReservationRetainsNullableIdAndCancelsByUuid(t *testing.T) {
	requestUUID := ""
	cancels, canonicalDeletes := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		reply := func(v any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": v})
		}
		if strings.HasSuffix(q.URL.Path, "/creation/capabilities") {
			reply(map[string]any{"contract": client.VolumeCreationContract, "reserveBeforeSubmit": true, "requestLookup": true, "cancelTombstone": true, "canonicalIdentity": "user-disk-v1"})
			return
		}
		if strings.Contains(q.URL.Path, "/creation/") {
			uuid := q.URL.Path[strings.LastIndex(q.URL.Path, "/")+1:]
			if requestUUID != "" && requestUUID != uuid {
				t.Error("unknown reservation changed UUID")
			}
			requestUUID = uuid
			if q.Method == http.MethodDelete {
				cancels++
				reply(creationView(uuid, "CANCELLED", nil))
				return
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		canonicalDeletes++
		t.Error("unknown identity reached canonical volume deletion")
	}))
	defer server.Close()
	r, req, resp := volumeCreationFixture(t, server.URL)
	r.Create(context.Background(), req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("unknown creation must remain an error with owned state")
	}
	var got VolumeResourceModel
	if d := resp.State.Get(context.Background(), &got); d.HasError() {
		t.Fatal(d)
	}
	if !got.ID.IsNull() || got.RequestUUID.ValueString() != requestUUID || requestUUID == "" {
		t.Fatalf("lost request checkpoint: %+v", got)
	}
	deleted := resource.DeleteResponse{State: resp.State}
	r.Delete(context.Background(), resource.DeleteRequest{State: resp.State}, &deleted)
	if deleted.Diagnostics.HasError() || cancels != 1 || canonicalDeletes != 0 {
		t.Fatalf("unsafe cancel: %v cancels=%d canonical=%d", deleted.Diagnostics, cancels, canonicalDeletes)
	}
}
