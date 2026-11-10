package resources

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"terraform-provider-prodata/internal/client"
)

func TestVmProtectionReservedSubmitCanBeRetriedByKnownIdentity(t *testing.T) {
	creates, submits, jobs := 0, 0, 0
	status := "RESERVED"
	r := &vmProtectionResource{kind: "backups", poll: time.Millisecond}
	r.client = protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
		if q.Method == "POST" && strings.HasSuffix(q.URL.Path, "/submit") {
			submits++
			if submits == 1 {
				w.WriteHeader(502)
				_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":502,"message":"request never delivered"}]}`))
				return
			}
			if status == "RESERVED" {
				jobs++
				status = "SUCCESS"
			}
		} else if q.Method == "POST" {
			creates++
		}
		protectionReply(w, protectionView("backups", status))
	})
	plan, state := protectionPlan(t, r, protectionInputs("backups"))
	out := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &out)
	if out.Diagnostics.HasError() || creates != 1 || submits != 2 || jobs != 1 {
		t.Fatalf("creates=%d submits=%d jobs=%d diagnostics=%v", creates, submits, jobs, out.Diagnostics)
	}
	checkIntAttribute(t, out.State, "id", 8)
}

func TestVmFromBackupReservedSubmitCanBeRetriedByKnownIdentity(t *testing.T) {
	creates, submits, jobs := 0, 0, 0
	status := "RESERVED"
	r := &VmFromBackupResource{poll: time.Millisecond}
	r.client = protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
		switch {
		case strings.HasSuffix(q.URL.Path, "/reservations"):
			creates++
			protectionReply(w, client.VmBackupRestoreResponse{VmID: 42})
		case strings.HasSuffix(q.URL.Path, "/submit"):
			submits++
			if submits == 1 {
				w.WriteHeader(502)
				_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":502,"message":"request never delivered"}]}`))
				return
			}
			if status == "RESERVED" {
				jobs++
				status = "NEW"
			}
			protectionReply(w, client.VmBackupRestoreOperation{Contract: "vm-protection-reservation-v1", VmID: 42, Status: status})
		case strings.Contains(q.URL.Path, "/restores/"):
			protectionReply(w, client.VmBackupRestoreOperation{Contract: "vm-protection-reservation-v1", VmID: 42, Status: status})
		default:
			protectionReply(w, client.Vm{ID: 42, Status: "STOPPED"})
		}
	})
	plan, state := protectionPlan(t, r, map[string]any{"backup_id": int64(7), "name": "recovery", "cpu_cores": int64(2), "ram": int64(16)})
	out := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &out)
	if out.Diagnostics.HasError() || creates != 1 || submits != 2 || jobs != 1 {
		t.Fatalf("creates=%d submits=%d jobs=%d diagnostics=%v", creates, submits, jobs, out.Diagnostics)
	}
	checkIntAttribute(t, out.State, "id", 42)
}

func TestVmProtectionUnverifiedReadWarnsWithoutBlockingTheWholeWorkspace(t *testing.T) {
	for _, status := range []string{"RESERVED", "UNVERIFIED"} {
		for _, kind := range []string{"backups", "snapshots"} {
			t.Run(kind+status, func(t *testing.T) {
				r := &vmProtectionResource{kind: kind}
				r.client = protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
					if q.Method != "GET" {
						t.Error("refresh must not submit")
					}
					protectionReply(w, protectionView(kind, status))
				})
				inputs := protectionInputs(kind)
				inputs["id"] = int64(8)
				inputs["status"] = status
				plan, _ := protectionPlan(t, r, inputs)
				state := tfsdk.State(plan)
				out := resource.ReadResponse{State: state}
				r.Read(context.Background(), resource.ReadRequest{State: state}, &out)
				if out.Diagnostics.HasError() || len(out.Diagnostics.Warnings()) == 0 || out.State.Raw.IsNull() {
					t.Fatalf("read blocks workspace: %v", out.Diagnostics)
				}
				checkIntAttribute(t, out.State, "id", 8)
			})
		}
		t.Run("restore"+status, func(t *testing.T) {
			r := &VmFromBackupResource{}
			r.client = protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
				if q.Method != "GET" {
					t.Error("refresh must not submit")
				}
				protectionReply(w, client.VmBackupRestoreOperation{Contract: "vm-protection-reservation-v1", VmID: 42, Status: status})
			})
			plan, _ := protectionPlan(t, r, map[string]any{"id": int64(42), "backup_id": int64(7), "name": "recovery", "cpu_cores": int64(2), "ram": int64(16), "restart_pending": false, "status": status})
			state := tfsdk.State(plan)
			out := resource.ReadResponse{State: state}
			r.Read(context.Background(), resource.ReadRequest{State: state}, &out)
			if out.Diagnostics.HasError() || len(out.Diagnostics.Warnings()) == 0 || out.State.Raw.IsNull() {
				t.Fatalf("read blocks workspace: %v", out.Diagnostics)
			}
			checkIntAttribute(t, out.State, "id", 42)
		})
	}
}
