package resources

import (
	"context"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"net/http"
	"net/http/httptest"
	"strings"
	"terraform-provider-prodata/internal/client"
	"testing"
	"time"
)

func attachmentView(status, direction string, exists bool) map[string]any {
	return map[string]any{"contract": client.VolumeAttachmentContract, "id": 515, "vmId": 77, "volumeId": 9, "exists": exists, "direction": direction, "status": status}
}
func volumeMoveClient(t *testing.T, h http.HandlerFunc) *client.Client {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/volume-attachments/capabilities") {
			protectionReply(w, map[string]any{"contract": client.VolumeAttachmentContract, "reserveBeforeSubmit": true, "recoverableSubmission": true})
			return
		}
		h(w, r)
	}))
	t.Cleanup(s.Close)
	c, e := client.New(client.Config{APIBaseURL: s.URL, APIKeyID: "fake", APISecretKey: "fake", Region: "TEST", ProjectTag: "project"})
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func attachmentInputs() map[string]any {
	return map[string]any{"vm_id": int64(77), "volume_id": int64(9), "region": "TEST", "project_tag": "project"}
}
func TestVolumeAttachmentCreateCheckpointsBeforeLostAcknowledgement(t *testing.T) {
	for _, lostAfterEffect := range []bool{false, true} {
		t.Run(map[bool]string{false: "submit not delivered", true: "ack lost after move"}[lostAfterEffect], func(t *testing.T) {
			submits, moves := 0, 0
			status := "RESERVED"
			r := &VolumeAttachmentResource{poll: time.Millisecond}
			r.client = volumeMoveClient(t, func(w http.ResponseWriter, q *http.Request) {
				switch {
				case strings.HasSuffix(q.URL.Path, "/reservations"):
					protectionReply(w, attachmentView("RESERVED", "ATTACH", true))
				case q.Method == "POST" && strings.HasSuffix(q.URL.Path, "/submit"):
					submits++
					if submits == 1 {
						if lostAfterEffect {
							moves++
							status = "SUCCESS"
						}
						w.WriteHeader(502)
						return
					}
					moves++
					status = "SUCCESS"
					protectionReply(w, attachmentView(status, "ATTACH", true))
				case q.Method == "GET":
					protectionReply(w, attachmentView(status, "ATTACH", true))
				default:
					t.Errorf("unexpected mutation %s %s", q.Method, q.URL.Path)
				}
			})
			plan, state := protectionPlan(t, r, attachmentInputs())
			out := resource.CreateResponse{State: state}
			r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &out)
			if out.Diagnostics.HasError() {
				t.Fatal(out.Diagnostics)
			}
			checkIntAttribute(t, out.State, "attached_volume_id", 515)
			if moves != 1 || submits != map[bool]int{false: 2, true: 1}[lostAfterEffect] {
				t.Fatalf("submits=%d,moves=%d", submits, moves)
			}
		})
	}
}
func TestVolumeAttachmentUnknownRetainsIdentityAndReadDoesNotMutate(t *testing.T) {
	posts := 0
	r := &VolumeAttachmentResource{poll: time.Millisecond}
	r.client = volumeMoveClient(t, func(w http.ResponseWriter, q *http.Request) {
		if q.Method == "POST" {
			posts++
			if strings.HasSuffix(q.URL.Path, "/reservations") {
				protectionReply(w, attachmentView("RESERVED", "ATTACH", true))
				return
			}
			w.WriteHeader(502)
			return
		}
		protectionReply(w, attachmentView("UNVERIFIED", "ATTACH", true))
	})
	plan, state := protectionPlan(t, r, attachmentInputs())
	created := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &created)
	if !created.Diagnostics.HasError() {
		t.Fatal("unknown create silently succeeded")
	}
	checkIntAttribute(t, created.State, "attached_volume_id", 515)
	before := posts
	read := resource.ReadResponse{State: created.State}
	r.Read(context.Background(), resource.ReadRequest{State: created.State}, &read)
	if read.Diagnostics.HasError() || read.Diagnostics.WarningsCount() != 1 || read.State.Raw.IsNull() || posts != before {
		t.Fatalf("read=%v posts=%d", read.Diagnostics, posts)
	}
}
func TestVolumeAttachmentDeleteDoesNotRepeatAmbiguousDetach(t *testing.T) {
	calls := 0
	status, direction := "SUCCESS", "ATTACH"
	r := &VolumeAttachmentResource{poll: time.Millisecond}
	r.client = volumeMoveClient(t, func(w http.ResponseWriter, q *http.Request) {
		if strings.HasSuffix(q.URL.Path, "/status") {
			protectionReply(w, map[string]any{"id": 77, "status": "STOPPED"})
			return
		}
		if q.Method == "DELETE" {
			calls++
			status, direction = "UNVERIFIED", "DETACH"
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":627,"message":"unverified disk move"}]}`))
			return
		}
		protectionReply(w, attachmentView(status, direction, true))
	})
	_, state := protectionPlan(t, r, attachmentInputs())
	model := VolumeAttachmentResourceModel{VmID: types.Int64Value(77), VolumeID: types.Int64Value(9), AttachedVolumeID: types.Int64Value(515), Region: types.StringValue("TEST"), ProjectTag: types.StringValue("project"), Status: types.StringValue("SUCCESS")}
	if ds := state.Set(context.Background(), &model); ds.HasError() {
		t.Fatal(ds)
	}
	out := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &out)
	if !out.Diagnostics.HasError() || calls != 1 || !out.State.Raw.Equal(state.Raw) {
		t.Fatalf("calls=%d diagnostics=%v", calls, out.Diagnostics)
	}
}
func TestVolumeAttachmentOldServerRejectsBeforeAnyMutation(t *testing.T) {
	mutations := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.Method != "GET" {
			mutations++
		}
		w.WriteHeader(404)
	}))
	defer s.Close()
	c, e := client.New(client.Config{APIBaseURL: s.URL, APIKeyID: "fake", APISecretKey: "fake"})
	if e != nil {
		t.Fatal(e)
	}
	r := &VolumeAttachmentResource{client: c}
	plan, state := protectionPlan(t, r, attachmentInputs())
	out := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &out)
	if !out.Diagnostics.HasError() || !out.State.Raw.IsNull() || mutations != 0 {
		t.Fatalf("mutations=%d diagnostics=%v", mutations, out.Diagnostics)
	}
}

func TestVolumeAttachmentConfirmedAbsentDeleteDrainsWithCapabilityDisabled(t *testing.T) {
	caps, mutations := 0, 0
	c := volumeMoveClient(t, func(w http.ResponseWriter, q *http.Request) {
		if strings.HasSuffix(q.URL.Path, "/capabilities") {
			caps++
			w.WriteHeader(503)
			return
		}
		if q.Method != "GET" {
			mutations++
		}
		protectionReply(w, attachmentView("DELETED", "DETACH", false))
	})
	r := &VolumeAttachmentResource{client: c}
	inputs := attachmentInputs()
	inputs["attached_volume_id"] = int64(515)
	plan, _ := protectionPlan(t, r, inputs)
	prior := tfsdk.State(plan)
	out := resource.DeleteResponse{State: prior}
	r.Delete(context.Background(), resource.DeleteRequest{State: prior}, &out)
	if out.Diagnostics.HasError() || caps != 0 || mutations != 0 {
		t.Fatalf("caps=%d mutations=%d diag=%v", caps, mutations, out.Diagnostics)
	}
}

func TestVolumeLegacyAssertionsUpdateOnlyMetadata(t *testing.T) {
	calls := 0
	c := volumeMoveClient(t, func(w http.ResponseWriter, q *http.Request) { calls++; w.WriteHeader(500) })
	r := &VolumeAttachmentResource{client: c}
	inputs := attachmentInputs()
	inputs["attached_volume_id"] = int64(515)
	plan, _ := protectionPlan(t, r, inputs)
	prior := tfsdk.State(plan)
	planned := tfsdk.Plan(prior)
	if d := planned.SetAttribute(context.Background(), path.Root("legacy_attached_volume_id"), "ceph:physical-root"); d.HasError() {
		t.Fatal(d)
	}
	out := resource.UpdateResponse{State: prior}
	r.Update(context.Background(), resource.UpdateRequest{State: prior, Plan: planned}, &out)
	var got VolumeAttachmentResourceModel
	if d := out.State.Get(context.Background(), &got); d.HasError() {
		t.Fatal(d)
	}
	if out.Diagnostics.HasError() || calls != 0 || got.LegacyAttachedVolumeID.ValueString() != "ceph:physical-root" || got.AttachedVolumeID.ValueInt64() != 515 {
		t.Fatalf("calls=%d model=%+v diag=%v", calls, got, out.Diagnostics)
	}
}

func TestVolumeDetachSubmitsStopOnlyOnceWhileVmStillReportsRunning(t *testing.T) {
	stops, starts, statusReads, deletes := 0, 0, 0, 0
	c := volumeMoveClient(t, func(w http.ResponseWriter, q *http.Request) {
		switch {
		case strings.HasSuffix(q.URL.Path, "/status"):
			statusReads++
			status := "RUNNING"
			if statusReads > 3 {
				status = "STOPPED"
			}
			protectionReply(w, map[string]any{"id": 77, "status": status})
		case strings.HasSuffix(q.URL.Path, "/stop"):
			stops++
			protectionReply(w, nil)
		case strings.HasSuffix(q.URL.Path, "/start"):
			starts++
			protectionReply(w, nil)
		case q.Method == "DELETE":
			deletes++
			protectionReply(w, attachmentView("DELETED", "DETACH", false))
		default:
			protectionReply(w, attachmentView("SUCCESS", "ATTACH", true))
		}
	})
	r := &VolumeAttachmentResource{client: c, poll: time.Millisecond}
	inputs := attachmentInputs()
	inputs["attached_volume_id"] = int64(515)
	plan, _ := protectionPlan(t, r, inputs)
	prior := tfsdk.State(plan)
	out := resource.DeleteResponse{State: prior}
	r.Delete(context.Background(), resource.DeleteRequest{State: prior}, &out)
	if out.Diagnostics.HasError() || stops != 1 || starts != 0 || deletes != 1 {
		t.Fatalf("stop=%d start=%d deletes=%d diag=%v", stops, starts, deletes, out.Diagnostics)
	}
}
