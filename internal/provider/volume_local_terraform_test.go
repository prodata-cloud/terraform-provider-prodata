package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"sync"
	"testing"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// The original regression needs the Terraform graph: a refresh of volume must not
// recreate it and consequently replace its attachment. All HTTP traffic is local.
func TestVolumeLocalTerraformStableIdentity(t *testing.T) {
	if _, err := exec.LookPath("terraform"); err != nil {
		t.Skip("Terraform CLI is not installed")
	}
	var mu sync.Mutex
	var exists, attached bool
	name := "data"
	creates, deletes, attaches, detaches := 0, 0, 0, 0
	stableVolume := func() map[string]any {
		data := map[string]any{"identityVersion": "user-disk-v1", "id": 9, "exists": exists}
		if exists {
			data["name"], data["type"], data["size"], data["inUse"] = name, "SSD", 10, attached
			if attached {
				data["attachedId"], data["attachmentId"] = 77, 515
			}
		}
		return data
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		answer := func(data any) { _ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data}) }
		if req.Header.Get("X-Project-Tag") != "test" || req.Header.Get("X-Region") != "TEST" {
			t.Errorf("wrong scope for %s %s", req.Method, req.URL.Path)
		}
		switch req.Method + " " + req.URL.Path {
		case "GET /panel-main/api/v2/volumes/by-user-disk/capabilities":
			answer(map[string]any{"identityVersion": "user-disk-v1"})
		case "GET /panel-main/api/v2/volumes/by-user-disk":
			items := []any{}
			if exists {
				items = append(items, stableVolume())
			}
			answer(map[string]any{"identityVersion": "user-disk-v1", "volumes": items})
		case "POST /panel-main/api/v2/volumes":
			var create client.CreateVolumeRequest
			if err := json.NewDecoder(req.Body).Decode(&create); err != nil {
				t.Error(err)
			}
			if exists {
				t.Error("refresh incorrectly planned a second disk")
			}
			exists, name = true, create.Name
			creates++
			answer(map[string]any{"id": 9, "name": name, "type": "SSD", "size": 10})
		case "GET /panel-main/api/v2/volumes/by-user-disk/9":
			answer(stableVolume())
		case "PUT /panel-main/api/v2/volumes/by-user-disk/9":
			var update client.UpdateVolumeRequest
			if err := json.NewDecoder(req.Body).Decode(&update); err != nil {
				t.Error(err)
			}
			name = update.Name
			answer(stableVolume())
		case "DELETE /panel-main/api/v2/volumes/by-user-disk/9":
			if attached {
				t.Error("volume deletion raced ahead of attachment destruction")
			}
			exists = false
			deletes++
			answer(stableVolume())
		case "POST /panel-main/api/v2/vms/77/volumes":
			var change client.AttachVolumeRequest
			if err := json.NewDecoder(req.Body).Decode(&change); err != nil || change.VolumeID != 9 {
				t.Errorf("attach did not use the canonical volume ID: %+v, %v", change, err)
			}
			attached = true
			attaches++
			answer(map[string]any{"id": 515, "name": name, "type": "SSD", "size": 10, "inUse": true, "attachedId": 77})
		case "GET /panel-main/api/v2/vms/77/volumes":
			items := []any{}
			if attached {
				items = append(items, map[string]any{"id": 515, "userDiskId": 9, "name": name, "size": 10, "type": "SSD", "bootDisk": false})
			}
			answer(items)
		case "GET /panel-main/api/v2/vms/77/status":
			answer(map[string]any{"id": 77, "status": "STOPPED"})
		case "DELETE /panel-main/api/v2/vms/77/volumes/515":
			attached = false
			detaches++
			answer(nil)
		default:
			t.Errorf("unexpected or ambiguous legacy request %s %s", req.Method, req.URL.Path)
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(server.Close)
	config := func(nextName string, count int) string {
		return fmt.Sprintf(`
provider "prodata" {
  api_base_url = %q
  api_key_id = "fake-key"
  api_secret_key = "fake-secret"
  region = "TEST"
  project_tag = "test"
}
resource "prodata_volume" "test" {
  name = %q
  type = "SSD"
  size = 10
}
resource "prodata_volume_attachment" "test" {
  count = %d
  vm_id = 77
  volume_id = prodata_volume.test.id
}
data "prodata_volume" "test" {
  id = prodata_volume.test.id
  depends_on = [prodata_volume_attachment.test]
}
data "prodata_volumes" "test" {
  depends_on = [prodata_volume.test, prodata_volume_attachment.test]
}
`, server.URL, nextName, count)
	}
	checks := func(expectedName string) []statecheck.StateCheck {
		return []statecheck.StateCheck{
			statecheck.ExpectKnownValue("prodata_volume.test", tfjsonpath.New("id"), knownvalue.Int64Exact(9)),
			statecheck.ExpectKnownValue("prodata_volume.test", tfjsonpath.New("name"), knownvalue.StringExact(expectedName)),
			statecheck.ExpectKnownValue("data.prodata_volume.test", tfjsonpath.New("id"), knownvalue.Int64Exact(9)),
		}
	}
	stable := resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(_ *terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if exists || attached || creates != 1 || deletes != 1 || attaches != 1 || detaches != 1 {
				return fmt.Errorf("unexpected lifecycle exists=%t attached=%t create/delete=%d/%d attach/detach=%d/%d", exists, attached, creates, deletes, attaches, detaches)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config("data", 0), ConfigStateChecks: checks("data"), ConfigPlanChecks: stable},
			{Config: config("data", 1), ConfigStateChecks: checks("data"), ConfigPlanChecks: stable},
			{Config: config("renamed", 1), ConfigStateChecks: checks("renamed"), ConfigPlanChecks: stable},
			{ResourceName: "prodata_volume.test", ImportState: true, ImportStateId: "9", ImportStateVerify: true},
			{Config: config("renamed", 0), ConfigStateChecks: checks("renamed"), ConfigPlanChecks: stable},
		},
	})
}
