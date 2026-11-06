package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func protectionPlan(t *testing.T, r resource.Resource, inputs map[string]any) (tfsdk.Plan, tfsdk.State) {
	t.Helper()
	var out resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &out)
	typ, ok := out.Schema.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("resource schema must be an object")
	}
	values := map[string]tftypes.Value{}
	for name, field := range typ.AttributeTypes {
		values[name] = tftypes.NewValue(field, inputs[name])
	}
	return tfsdk.Plan{Schema: out.Schema, Raw: tftypes.NewValue(typ, values)}, tfsdk.State{Schema: out.Schema, Raw: tftypes.NewValue(typ, nil)}
}
func protectionClient(t *testing.T, h http.HandlerFunc) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.URL.Path == "/panel-main/api/v2/vm-protection/capabilities" {
			protectionReply(w, map[string]any{"contract": "vm-protection-reservation-v1", "reserveBeforeSubmit": true, "singleSubmit": true, "restoreIdentityBeforeSubmit": true})
			return
		}
		h(w, q)
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(client.Config{APIBaseURL: srv.URL, APIKeyID: "fake", APISecretKey: "fake", Region: "TEST", ProjectTag: "project"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func protectionReply(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
}
func protectionInputs(kind string) map[string]any {
	m := map[string]any{"vm_id": int64(5), "name": "backup_one", "mode": "snapshot", "include_ram": false, "cron": "0 0 2 * * *"}
	if kind == "snapshots" {
		m["name"] = "snapshot_one"
	}
	return m
}
func protectionView(kind, status string) client.VmProtection {
	return client.VmProtection{Contract: "vm-protection-reservation-v1", ID: 8, VmID: 5, Present: true, Name: fmt.Sprint(protectionInputs(kind)["name"]), Mode: "snapshot", Cron: "0 0 2 * * *", Status: status}
}
func checkIntAttribute(t *testing.T, s tfsdk.State, key string, want int64) {
	t.Helper()
	var got int64
	if ds := s.GetAttribute(context.Background(), path.Root(key), &got); ds.HasError() || got != want {
		t.Fatalf("%s=%d, want %d: %v", key, got, want, ds)
	}
}

func TestVmProtectionCreateAllKindsWaitAndStoreIdentity(t *testing.T) {
	for _, kind := range []string{"backups", "snapshots", "backup-schedules"} {
		t.Run(kind, func(t *testing.T) {
			gets, posts := 0, 0
			r := &vmProtectionResource{kind: kind, poll: time.Millisecond}
			r.client = protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
				if q.Header.Get("X-Region") != "TEST" || q.Header.Get("X-Project-Tag") != "project" {
					t.Errorf("scope missing: %v", q.Header)
				}
				if !strings.HasPrefix(q.URL.Path, "/panel-main/api/v2/vms/5/"+kind) {
					t.Errorf("wrong path %s", q.URL.Path)
				}
				status := "NEW"
				if kind == "backup-schedules" {
					status = "SCHEDULE"
				}
				switch q.Method {
				case "POST":
					if strings.HasSuffix(q.URL.Path, "/submit") {
						protectionReply(w, protectionView(kind, status))
						return
					}
					posts++
					var body client.VmProtectionRequest
					_ = json.NewDecoder(q.Body).Decode(&body)
					if body.Name == "" {
						t.Error("missing name")
					}
				case "GET":
					gets++
					if gets > 1 && kind != "backup-schedules" {
						status = "SUCCESS"
					}
				default:
					t.Errorf("unexpected method %s", q.Method)
				}
				protectionReply(w, protectionView(kind, status))
			})
			plan, state := protectionPlan(t, r, protectionInputs(kind))
			out := resource.CreateResponse{State: state}
			r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &out)
			if out.Diagnostics.HasError() {
				t.Fatal(out.Diagnostics)
			}
			checkIntAttribute(t, out.State, "id", 8)
			checkIntAttribute(t, out.State, "vm_id", 5)
			if posts != 1 || gets < 1 {
				t.Fatalf("posts=%d gets=%d", posts, gets)
			}
		})
	}
}
func TestVmProtectionCreateFailedJobKeepsDurableId(t *testing.T) {
	r := &vmProtectionResource{kind: "backups", poll: time.Millisecond}
	r.client = protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
		status := "NEW"
		if q.Method == "GET" {
			status = "FAIL"
		}
		protectionReply(w, protectionView("backups", status))
	})
	plan, state := protectionPlan(t, r, protectionInputs("backups"))
	out := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &out)
	if !out.Diagnostics.HasError() {
		t.Fatal("failed job reported success")
	}
	checkIntAttribute(t, out.State, "id", 8)
	checkIntAttribute(t, out.State, "vm_id", 5)
}
func TestVmProtectionReadOnlyVerifiedTombstoneRemovesState(t *testing.T) {
	for _, mode := range []string{"absent", "legacy404", "forbidden", "bad-contract", "missing-present", "wrong-vm", "present"} {
		t.Run(mode, func(t *testing.T) {
			r := &vmProtectionResource{kind: "backups"}
			r.client = protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
				v := protectionView("backups", "SUCCESS")
				switch mode {
				case "absent":
					v.Present = false
				case "legacy404":
					http.NotFound(w, q)
					return
				case "forbidden":
					w.WriteHeader(403)
					_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":714,"message":"forbidden"}]}`))
					return
				case "bad-contract":
					v.Contract = ""
				case "missing-present":
					protectionReply(w, map[string]any{"contract": "vm-protection-reservation-v1", "id": 8, "vmId": 5})
					return
				case "wrong-vm":
					v.VmID = 77
				}
				protectionReply(w, v)
			})
			inputs := protectionInputs("backups")
			inputs["id"] = int64(8)
			plan, _ := protectionPlan(t, r, inputs)
			prior := tfsdk.State(plan)
			out := resource.ReadResponse{State: prior}
			r.Read(context.Background(), resource.ReadRequest{State: prior}, &out)
			if mode == "absent" {
				if !out.State.Raw.IsNull() {
					t.Fatal("verified tombstone kept state")
				}
				return
			}
			checkIntAttribute(t, out.State, "id", 8)
			checkIntAttribute(t, out.State, "vm_id", 5)
			if (mode != "present") != out.Diagnostics.HasError() {
				t.Fatalf("mode %s diagnostics %v", mode, out.Diagnostics)
			}
		})
	}
}
func TestVmProtectionDeleteNeedsConfirmation(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		t.Run(fmt.Sprint(confirmed), func(t *testing.T) {
			deleted := false
			r := &vmProtectionResource{kind: "snapshots"}
			r.client = protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
				if q.Method == "DELETE" {
					deleted = true
					protectionReply(w, nil)
					return
				}
				v := protectionView("snapshots", "SUCCESS")
				v.Present = !deleted || !confirmed
				protectionReply(w, v)
			})
			inputs := protectionInputs("snapshots")
			inputs["id"] = int64(8)
			plan, _ := protectionPlan(t, r, inputs)
			prior := tfsdk.State(plan)
			out := resource.DeleteResponse{State: prior}
			r.Delete(context.Background(), resource.DeleteRequest{State: prior}, &out)
			if !deleted || out.Diagnostics.HasError() == confirmed {
				t.Fatalf("delete=%v diagnostics=%v", deleted, out.Diagnostics)
			}
		})
	}
}
func TestVmProtectionScheduleUpdateIsInPlace(t *testing.T) {
	r := &vmProtectionResource{kind: "backup-schedules"}
	r.client = protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
		if q.Method != "PUT" || !strings.HasSuffix(q.URL.Path, "/8") {
			t.Errorf("request %s %s", q.Method, q.URL.Path)
		}
		v := protectionView(r.kind, "SCHEDULE")
		v.Cron = "0 30 4 * * *"
		protectionReply(w, v)
	})
	inputs := protectionInputs(r.kind)
	inputs["id"] = int64(8)
	inputs["cron"] = "0 30 4 * * *"
	plan, state := protectionPlan(t, r, inputs)
	out := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan}, &out)
	if out.Diagnostics.HasError() {
		t.Fatal(out.Diagnostics)
	}
	checkIntAttribute(t, out.State, "id", 8)
	checkIntAttribute(t, out.State, "vm_id", 5)
}
func TestVmProtectionImportRejectsAmbiguousIds(t *testing.T) {
	r := &vmProtectionResource{kind: "backups", client: &client.Client{Region: "TEST", ProjectTag: "project"}}
	for _, id := range []string{"8", "0:8", "5:0", "-1:8", "5:no", "5:8:9", "5:8"} {
		t.Run(id, func(t *testing.T) {
			_, state := protectionPlan(t, r, map[string]any{})
			out := resource.ImportStateResponse{State: state}
			r.ImportState(context.Background(), resource.ImportStateRequest{ID: id}, &out)
			if (id != "5:8") != out.Diagnostics.HasError() {
				t.Fatalf("id=%s diagnostics=%v", id, out.Diagnostics)
			}
		})
	}
}
func TestVmFromBackupCreateKeepsVmOnAsyncFailure(t *testing.T) {
	for _, status := range []string{"RUNNING", "ERROR"} {
		t.Run(status, func(t *testing.T) {
			r := &VmFromBackupResource{poll: time.Millisecond}
			reserves, submits := 0, 0
			r.client = protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
				if strings.HasSuffix(q.URL.Path, "/reservations") {
					reserves++
					var body client.VmBackupRestoreRequest
					_ = json.NewDecoder(q.Body).Decode(&body)
					if body.RAM != 16 || body.CPUCores != 2 {
						t.Errorf("restore shape %+v", body)
					}
					protectionReply(w, client.VmBackupRestoreResponse{VmID: 42, TaskUUID: "tf-protection:reserved:token"})
					return
				}
				if strings.HasSuffix(q.URL.Path, "/submit") {
					submits++
					protectionReply(w, client.VmBackupRestoreOperation{Contract: "vm-protection-reservation-v1", VmID: 42, Status: "NEW", Password: "generated"})
					return
				}
				protectionReply(w, client.Vm{ID: 42, Name: "restored-vm", Status: status, CPUCores: 2, RAM: 16})
			})
			plan, state := protectionPlan(t, r, map[string]any{"backup_id": int64(8), "name": "restored-vm", "cpu_cores": int64(2), "ram": int64(16)})
			out := resource.CreateResponse{State: state}
			r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &out)
			if (status == "ERROR") != out.Diagnostics.HasError() {
				t.Fatalf("status %s diagnostics %v", status, out.Diagnostics)
			}
			checkIntAttribute(t, out.State, "id", 42)
			if reserves != 1 || submits != 1 {
				t.Fatal("restore reserved/submitted more than once")
			}
		})
	}
}

func TestVmProtectionTimeoutRetainsDurableId(t *testing.T) {
	r := &vmProtectionResource{kind: "backups", poll: time.Millisecond}
	entered := make(chan struct{})
	r.client = protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
		if q.Method == "GET" {
			close(entered)
			<-q.Context().Done()
			return
		}
		protectionReply(w, protectionView("backups", "NEW"))
	})
	plan, state := protectionPlan(t, r, protectionInputs("backups"))
	out := resource.CreateResponse{State: state}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		select {
		case <-entered:
			cancel()
		case <-ctx.Done():
		}
	}()
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &out)
	if !out.Diagnostics.HasError() {
		t.Fatal("timeout reported success")
	}
	checkIntAttribute(t, out.State, "id", 8)
}
func TestVmProtectionDeleteCanRemoveJobThatFailedWhileWaiting(t *testing.T) {
	gets, deleted := 0, false
	r := &vmProtectionResource{kind: "backups", poll: time.Millisecond}
	r.client = protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
		if q.Method == "DELETE" {
			deleted = true
			protectionReply(w, nil)
			return
		}
		gets++
		v := protectionView("backups", "NEW")
		if gets > 1 {
			v.Status = "FAIL"
		}
		v.Present = !deleted
		protectionReply(w, v)
	})
	inputs := protectionInputs("backups")
	inputs["id"] = int64(8)
	plan, _ := protectionPlan(t, r, inputs)
	prior := tfsdk.State(plan)
	out := resource.DeleteResponse{State: prior}
	r.Delete(context.Background(), resource.DeleteRequest{State: prior}, &out)
	if out.Diagnostics.HasError() || !deleted {
		t.Fatalf("failed job not deleted: %v", out.Diagnostics)
	}
}

func TestVmProtectionLostSubmitAcknowledgementNeverCreatesSecondJob(t *testing.T) {
	creates, submits, jobs := 0, 0, 0
	status := "RESERVED"
	r := &vmProtectionResource{kind: "backups", poll: time.Millisecond}
	r.client = protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
		if q.Method == "POST" && strings.HasSuffix(q.URL.Path, "/submit") {
			submits++
			if status == "RESERVED" {
				jobs++
				status = "SUCCESS"
			}
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":503,"message":"acknowledgement lost"}]}`))
			return
		}
		if q.Method == "POST" {
			creates++
		}
		protectionReply(w, protectionView("backups", status))
	})
	plan, state := protectionPlan(t, r, protectionInputs("backups"))
	out := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &out)
	if out.Diagnostics.HasError() {
		t.Fatal(out.Diagnostics)
	}
	checkIntAttribute(t, out.State, "id", 8)
	refreshed := resource.ReadResponse{State: out.State}
	r.Read(context.Background(), resource.ReadRequest{State: out.State}, &refreshed)
	if refreshed.Diagnostics.HasError() {
		t.Fatal(refreshed.Diagnostics)
	}
	if creates != 1 || submits != 1 || jobs != 1 {
		t.Fatalf("create=%d submit=%d jobs=%d", creates, submits, jobs)
	}
}

func TestVmFromBackupLostSubmitAckRetainsIdentityAndNeverResubmits(t *testing.T) {
	reserves, submits, jobs := 0, 0, 0
	r := &VmFromBackupResource{poll: time.Millisecond}
	r.client = protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
		switch {
		case strings.HasSuffix(q.URL.Path, "/reservations"):
			reserves++
			protectionReply(w, client.VmBackupRestoreResponse{VmID: 42})
		case strings.HasSuffix(q.URL.Path, "/submit"):
			submits++
			jobs++
			w.WriteHeader(503)
		case strings.Contains(q.URL.Path, "/restores/"):
			protectionReply(w, client.VmBackupRestoreOperation{Contract: "vm-protection-reservation-v1", VmID: 42, Status: "RUNNING"})
		default:
			protectionReply(w, client.Vm{ID: 42, Name: "restored-vm", Status: "RUNNING", CPUCores: 2, RAM: 16})
		}
	})
	plan, state := protectionPlan(t, r, map[string]any{"backup_id": int64(8), "name": "restored-vm", "cpu_cores": int64(2), "ram": int64(16)})
	out := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &out)
	if out.Diagnostics.HasError() {
		t.Fatal(out.Diagnostics)
	}
	refresh := resource.ReadResponse{State: out.State}
	r.Read(context.Background(), resource.ReadRequest{State: out.State}, &refresh)
	if refresh.Diagnostics.HasError() {
		t.Fatal(refresh.Diagnostics)
	}
	checkIntAttribute(t, refresh.State, "id", 42)
	if reserves != 1 || submits != 1 || jobs != 1 {
		t.Fatalf("reserve=%d submit=%d jobs=%d", reserves, submits, jobs)
	}
}

func TestVmFromBackupUpdatesMutableInputsWithoutRestoringAgain(t *testing.T) {
	for _, resize := range []bool{false, true} {
		t.Run(fmt.Sprint(resize), func(t *testing.T) {
			current := client.Vm{ID: 42, Name: "original-vm", Description: "before", Status: "RUNNING", CPUCores: 2, RAM: 16}
			stops, starts, patches := 0, 0, 0
			r := &VmFromBackupResource{poll: time.Millisecond}
			r.client = protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
				switch {
				case strings.Contains(q.URL.Path, "/restores/"):
					protectionReply(w, client.VmBackupRestoreOperation{Contract: "vm-protection-reservation-v1", VmID: 42, Status: current.Status})
				case strings.HasSuffix(q.URL.Path, "/status"):
					protectionReply(w, current)
				case strings.HasSuffix(q.URL.Path, "/stop"):
					stops++
					current.Status = "STOPPED"
					protectionReply(w, nil)
				case strings.HasSuffix(q.URL.Path, "/start"):
					starts++
					current.Status = "RUNNING"
					protectionReply(w, nil)
				case q.Method == "PATCH":
					patches++
					var body client.Vm
					_ = json.NewDecoder(q.Body).Decode(&body)
					if strings.HasSuffix(q.URL.Path, "/resources") {
						current.CPUCores = body.CPUCores
						current.RAM = body.RAM
					}
					if strings.HasSuffix(q.URL.Path, "/name") {
						current.Name = body.Name
					}
					if strings.HasSuffix(q.URL.Path, "/description") {
						current.Description = body.Description
					}
					protectionReply(w, nil)
				default:
					t.Errorf("unexpected request %s %s", q.Method, q.URL.Path)
					w.WriteHeader(500)
				}
			})
			values := map[string]any{"id": int64(42), "backup_id": int64(8), "name": "original-vm", "description": "before", "cpu_cores": int64(2), "ram": int64(16), "region": "TEST", "project_tag": "project", "password": "generated-original", "restart_pending": false, "status": "RUNNING"}
			prior, _ := protectionPlan(t, r, values)
			values["name"] = "renamed-vm"
			values["description"] = "after"
			if resize {
				values["cpu_cores"] = int64(4)
				values["ram"] = int64(32)
			}
			plan, _ := protectionPlan(t, r, values)
			out := resource.UpdateResponse{State: tfsdk.State(prior)}
			r.Update(context.Background(), resource.UpdateRequest{State: tfsdk.State(prior), Plan: plan}, &out)
			if out.Diagnostics.HasError() {
				t.Fatal(out.Diagnostics)
			}
			checkIntAttribute(t, out.State, "id", 42)
			if (resize && (stops != 1 || starts != 1 || patches != 3)) || (!resize && (stops != 0 || starts != 0 || patches != 2)) {
				t.Fatalf("resize=%v stops=%d starts=%d patches=%d", resize, stops, starts, patches)
			}
			var password string
			out.State.GetAttribute(context.Background(), path.Root("password"), &password)
			if password != "generated-original" {
				t.Fatalf("password changed %s", password)
			}
		})
	}
}

func TestVmFromBackupFailedResizeAndReadbackKeepsOnlyVerifiedState(t *testing.T) {
	r := &VmFromBackupResource{poll: time.Millisecond}
	reads := 0
	r.client = protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
		switch {
		case strings.Contains(q.URL.Path, "/restores/"):
			protectionReply(w, client.VmBackupRestoreOperation{Contract: "vm-protection-reservation-v1", VmID: 42, Status: "STOPPED"})
		case strings.HasSuffix(q.URL.Path, "/status"):
			reads++
			if reads > 2 {
				w.WriteHeader(503)
				return
			}
			protectionReply(w, client.Vm{ID: 42, Name: "original-vm", Status: "STOPPED", CPUCores: 2, RAM: 16})
		case strings.HasSuffix(q.URL.Path, "/resources"):
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":705,"message":"quota"}]}`))
		default:
			t.Errorf("unexpected mutation %s %s", q.Method, q.URL.Path)
			w.WriteHeader(500)
		}
	})
	values := map[string]any{"id": int64(42), "backup_id": int64(8), "name": "original-vm", "cpu_cores": int64(2), "ram": int64(16), "region": "TEST", "project_tag": "project", "restart_pending": false, "status": "STOPPED"}
	prior, _ := protectionPlan(t, r, values)
	values["cpu_cores"] = int64(4)
	values["ram"] = int64(32)
	values["status"] = tftypes.UnknownValue
	plan, _ := protectionPlan(t, r, values)
	out := resource.UpdateResponse{State: tfsdk.State(prior)}
	r.Update(context.Background(), resource.UpdateRequest{State: tfsdk.State(prior), Plan: plan}, &out)
	if !out.Diagnostics.HasError() {
		t.Fatal("failed resize reported success")
	}
	checkIntAttribute(t, out.State, "cpu_cores", 2)
	checkIntAttribute(t, out.State, "ram", 16)
	var status string
	if ds := out.State.GetAttribute(context.Background(), path.Root("status"), &status); ds.HasError() || status != "STOPPED" {
		t.Fatalf("unverified status persisted: %s %v", status, ds)
	}
}
