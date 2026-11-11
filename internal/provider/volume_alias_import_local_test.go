package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"terraform-provider-prodata/internal/client"
)

// Import must use the resource's selected provider, including both scope fields.
// Everything already exists: any mutation or name/list-based lookup is a failure.
func TestVolumeAliasLocalTerraformImportsExistingVolumeAndAttachment(t *testing.T) {
	tf, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("local Terraform binary not installed")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", tf)
	t.Setenv("TF_ACC_TERRAFORM_VERSION", "")
	t.Setenv("CHECKPOINT_DISABLE", "1")
	var mu sync.Mutex
	seen := map[string]int{}
	mutations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if request.Method != http.MethodGet {
			mutations++
			t.Errorf("import attempted a mutation: %s %s", request.Method, request.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if request.Header.Get("X-Region") != "ALIAS-TEST" || request.Header.Get("X-Project-Tag") != "alias-project" {
			t.Errorf("import used default or incomplete scope: region=%q project=%q", request.Header.Get("X-Region"), request.Header.Get("X-Project-Tag"))
			w.WriteHeader(http.StatusForbidden)
			return
		}
		seen[request.URL.Path]++
		answer := func(data any) { _ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data}) }
		switch request.URL.Path {
		case "/panel-main/api/v2/volumes/by-user-disk/9":
			answer(map[string]any{"identityVersion": "user-disk-v1", "id": 9, "exists": true, "name": "already-existing", "type": "SSD", "size": 10, "inUse": true, "attachedId": 77, "attachmentId": 515})
		case "/panel-main/api/v2/vms/77/volumes":
			answer([]map[string]any{{"id": 515, "userDiskId": 9, "name": "already-existing", "type": "SSD", "size": 10, "bootDisk": false}})
		case "/panel-main/api/v2/vms/77/status":
			answer(map[string]any{"id": 77, "status": "STOPPED"})
		case "/panel-main/api/v2/vms/77/volume-attachments/515":
			answer(map[string]any{"contract": client.VolumeAttachmentContract, "id": 515, "vmId": 77, "volumeId": 9, "exists": true, "direction": "ATTACH", "status": "SUCCESS"})
		default:
			t.Errorf("import used an unexpected or name-based route: %s", request.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	providers := fmt.Sprintf(`
provider "prodata" {
  api_base_url = %q
  api_key_id = "fake-key"
  api_secret_key = "fake-secret"
  region = "WRONG-DEFAULT"
  project_tag = "wrong-default-project"
}
provider "prodata" {
  alias = "target"
  api_base_url = %q
  api_key_id = "fake-key"
  api_secret_key = "fake-secret"
  region = "ALIAS-TEST"
  project_tag = "alias-project"
}
`, server.URL, server.URL)
	config := providers + `
resource "prodata_volume" "existing" {
  provider = prodata.target
  name = "already-existing"
  type = "SSD"
  size = 10
}
resource "prodata_volume_attachment" "existing" {
  provider = prodata.target
  vm_id = 77
  volume_id = prodata_volume.existing.id
}
`
	imports := `
import {
  to = prodata_volume.existing
  id = "9"
}
import {
  to = prodata_volume_attachment.existing
  id = "77:9"
}
`
	// Remove only local Terraform ownership after the test; imported infrastructure
	// must receive no DELETE, detach, stop, or other mutation during cleanup either.
	forget := providers + `
removed {
  from = prodata_volume_attachment.existing
  lifecycle { destroy = false }
}
removed {
  from = prodata_volume.existing
  lifecycle { destroy = false }
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(_ *terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if mutations != 0 {
				return fmt.Errorf("import/forget made %d mutations", mutations)
			}
			for _, path := range []string{"/panel-main/api/v2/volumes/by-user-disk/9", "/panel-main/api/v2/vms/77/volumes", "/panel-main/api/v2/vms/77/volume-attachments/515"} {
				if seen[path] == 0 {
					return fmt.Errorf("expected exact-identity import read was not exercised: %s", path)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: config + imports,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("prodata_volume.existing", "id", "9"),
					resource.TestCheckResourceAttr("prodata_volume.existing", "region", "ALIAS-TEST"),
					resource.TestCheckResourceAttr("prodata_volume.existing", "project_tag", "alias-project"),
					resource.TestCheckResourceAttr("prodata_volume_attachment.existing", "attached_volume_id", "515"),
					resource.TestCheckResourceAttr("prodata_volume_attachment.existing", "region", "ALIAS-TEST"),
					resource.TestCheckResourceAttr("prodata_volume_attachment.existing", "project_tag", "alias-project")),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("prodata_volume.existing", plancheck.ResourceActionNoop),
					plancheck.ExpectResourceAction("prodata_volume_attachment.existing", plancheck.ResourceActionNoop),
				}, PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
			{ResourceName: "prodata_volume.existing", ImportState: true, ImportStateId: "9", ImportStateVerify: true},
			{ResourceName: "prodata_volume_attachment.existing", ImportState: true, ImportStateId: "77:9", ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "attached_volume_id"},
			{Config: config, PlanOnly: true},
			{Config: forget},
		},
	})
}
