package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	frameworkresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"terraform-provider-prodata/internal/client"
	"terraform-provider-prodata/internal/provider/resources"
	"testing"
)

// This provider adds only the task's resources without changing the shared registry during
// parallel implementation. It exercises the actual Terraform protocol and plan consistency.
type protectionTestProvider struct{ ProDataProvider }

func (p *protectionTestProvider) Resources(_ context.Context) []func() frameworkresource.Resource {
	return []func() frameworkresource.Resource{resources.NewVmBackupResource, resources.NewVmSnapshotResource, resources.NewVmBackupScheduleResource, resources.NewVmFromBackupResource}
}
func TestVmProtectionLocalTerraform(t *testing.T) {
	tf, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("local Terraform binary not installed")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", tf)
	t.Setenv("TF_ACC_TERRAFORM_VERSION", "")
	var mu sync.Mutex
	objects := map[string]client.VmProtection{}
	vmExists := false
	vmID, restores, vmRAM, vmCPU := int64(42), 0, int64(16), int64(2)
	vmName, vmDescription := "restored-vm", ""
	vmDeletes := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		success := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": v}) }
		p := strings.TrimPrefix(q.URL.Path, "/panel-main/api/v2/")
		if p == "vm-protection/capabilities" {
			success(map[string]any{"contract": "vm-protection-reservation-v1", "reserveBeforeSubmit": true, "singleSubmit": true, "restoreIdentityBeforeSubmit": true})
			return
		}
		if p == "vm-backups/8/restore-as-new-vm/reservations" && q.Method == "POST" {
			vmExists = true
			restores++
			vmID = int64(41 + restores)
			var request client.VmBackupRestoreRequest
			if err := json.NewDecoder(q.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			vmRAM = request.RAM
			vmCPU = request.CPUCores
			vmName = request.Name
			vmDescription = request.Description
			success(client.VmBackupRestoreResponse{VmID: vmID, TaskUUID: "task", Password: fmt.Sprintf("generated-%d", restores)})
			return
		}
		if strings.HasPrefix(p, fmt.Sprintf("vm-backups/8/restores/%d", vmID)) {
			if strings.HasSuffix(p, "/reservation") {
				success(false)
				return
			}
			success(client.VmBackupRestoreOperation{Contract: "vm-protection-reservation-v1", VmID: vmID, Status: "STOPPED", Password: "generated-1"})
			return
		}
		if q.Method == "PATCH" && strings.HasPrefix(p, fmt.Sprintf("vms/%d/", vmID)) {
			var body client.Vm
			_ = json.NewDecoder(q.Body).Decode(&body)
			switch {
			case strings.HasSuffix(p, "/resources"):
				vmCPU = body.CPUCores
				vmRAM = body.RAM
			case strings.HasSuffix(p, "/name"):
				vmName = body.Name
			case strings.HasSuffix(p, "/description"):
				vmDescription = body.Description
			default:
				t.Errorf("unexpected VM mutation %s", p)
			}
			success(nil)
			return
		}
		if p == fmt.Sprintf("vms/%d/status", vmID) {
			if !vmExists {
				w.WriteHeader(404)
				_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]any{{"code": 601, "message": "VM not found"}}})
				return
			}
			success(client.Vm{ID: vmID, Name: vmName, Description: vmDescription, Status: "STOPPED", CPUCores: vmCPU, RAM: vmRAM})
			return
		}
		if p == fmt.Sprintf("vms/%d", vmID) && q.Method == "DELETE" {
			vmExists = false
			vmDeletes++
			success(nil)
			return
		}
		parts := strings.Split(p, "/")
		if len(parts) < 3 || parts[0] != "vms" || parts[1] != "5" {
			t.Errorf("unexpected route %s %s", q.Method, p)
			w.WriteHeader(500)
			return
		}
		kind := parts[2]
		v, ok := objects[kind]
		if q.Method == "POST" && strings.HasSuffix(p, "/submit") {
			if !ok {
				t.Error("submit before durable reservation")
				w.WriteHeader(500)
				return
			}
			v.Status = "SUCCESS"
			objects[kind] = v
			success(v)
			return
		}
		switch q.Method {
		case "POST", "PUT":
			var req client.VmProtectionRequest
			if e := json.NewDecoder(q.Body).Decode(&req); e != nil {
				t.Error(e)
			}
			v = client.VmProtection{Contract: "vm-protection-reservation-v1", ID: 8, VmID: 5, Present: true, Name: req.Name, Mode: req.Mode, Cron: req.Cron, Status: "SUCCESS"}
			if req.IncludeRAM != nil {
				v.IncludeRAM = *req.IncludeRAM
			}
			if kind == "backup-schedules" {
				v.Status = "SCHEDULE"
			} else {
				v.Status = "RESERVED"
			}
			objects[kind] = v
			success(v)
		case "GET":
			if !ok {
				v = client.VmProtection{Contract: "vm-protection-reservation-v1", ID: 8, VmID: 5, Present: false, Status: "DELETED"}
			}
			success(v)
		case "DELETE":
			delete(objects, kind)
			success(nil)
		default:
			t.Errorf("unexpected method %s", q.Method)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	config := func(cron string) string {
		return fmt.Sprintf(`
 provider "prodata" {
   api_base_url = %q
   api_key_id = "fake-key"
   api_secret_key = "fake-secret"
   region = "TEST"
   project_tag = "project"
 }
 resource "prodata_vm_backup" "point" {
   vm_id = 5
   name = "before-maintenance"
 }
 resource "prodata_vm_snapshot" "point" {
   vm_id = 5
   name = "before_change"
 }
 resource "prodata_vm_backup_schedule" "nightly" {
   vm_id = 5
   name = "nightly"
   cron = %q
 }
 resource "prodata_vm_from_backup" "restored" {
   backup_id = prodata_vm_backup.point.id
   name = "restored-vm"
   cpu_cores = 2
   ram = 16
 }
 `, srv.URL, cron)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"prodata": providerserver.NewProtocol6WithError(&protectionTestProvider{})},
		Steps: []resource.TestStep{
			{Config: config("0 0 2 * * *"), ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{Config: config("0 30 4 * * *"), ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{ResourceName: "prodata_vm_backup.point", ImportState: true, ImportStateId: "5:8", ImportStateVerify: true},
			{ResourceName: "prodata_vm_snapshot.point", ImportState: true, ImportStateId: "5:8", ImportStateVerify: true},
			{ResourceName: "prodata_vm_backup_schedule.nightly", ImportState: true, ImportStateId: "5:8", ImportStateVerify: true},
			{ResourceName: "prodata_vm_from_backup.restored", ImportState: true, ImportStateId: "8:42", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"password"}},
			{Config: strings.Replace(strings.Replace(strings.Replace(config("0 30 4 * * *"), "ram = 16", "ram = 32", 1), "name = \"restored-vm\"", "name = \"restored-renamed\"\n description = \"updated metadata\"", 1), "cpu_cores = 2", "cpu_cores = 4", 1), Check: func(_ *terraform.State) error {
				if restores != 1 || vmID != 42 || vmDeletes != 0 {
					return fmt.Errorf("mutable update recreated VM: restores=%d ID=%d deletes=%d", restores, vmID, vmDeletes)
				}
				return nil
			}, ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		},
	})
}
