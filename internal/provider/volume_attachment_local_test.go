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
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"terraform-provider-prodata/internal/client"
	"terraform-provider-prodata/internal/provider/resources"
	"testing"
)

type volumeMoveTestProvider struct{ ProDataProvider }

func (p *volumeMoveTestProvider) Resources(context.Context) []func() frameworkresource.Resource {
	return []func() frameworkresource.Resource{resources.NewVolumeAttachmentResource}
}
func TestVolumeAttachmentLocalTerraformLostAckReadImportAndDestroy(t *testing.T) {
	tf, e := exec.LookPath("terraform")
	if e != nil {
		t.Skip("local Terraform binary not installed")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", tf)
	t.Setenv("TF_ACC_TERRAFORM_VERSION", "")
	var mu sync.Mutex
	status, direction := "RESERVED", "ATTACH"
	exists := false
	reserves, submits, moves, deletes := 0, 0, 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		success := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": v}) }
		view := func() {
			success(map[string]any{"contract": client.VolumeAttachmentContract, "id": 515, "vmId": 77, "volumeId": 9, "exists": exists, "direction": direction, "status": status})
		}
		p := strings.TrimPrefix(q.URL.Path, "/panel-main/api/v2/")
		switch {
		case p == "vms/77/volume-attachments/capabilities":
			success(map[string]any{"contract": client.VolumeAttachmentContract, "reserveBeforeSubmit": true, "recoverableSubmission": true})
		case p == "vms/77/volume-attachments/reservations" && q.Method == "POST":
			reserves++
			exists = true
			status = "RESERVED"
			view()
		case p == "vms/77/volume-attachments/515/submit" && q.Method == "POST":
			submits++
			moves++
			status = "SUCCESS"
			w.WriteHeader(502) // Physical result committed; its HTTP acknowledgement was lost.
		case p == "vms/77/volume-attachments/515" && q.Method == "GET":
			view()
		case p == "vms/77/volume-attachments/515" && q.Method == "DELETE":
			deletes++
			direction = "DETACH"
			exists = false
			status = "DELETED"
			view()
		case p == "vms/77/status" && q.Method == "GET":
			success(map[string]any{"id": 77, "status": "STOPPED"})
		case p == "vms/77/volumes" && q.Method == "GET":
			success([]map[string]any{{"id": 515, "userDiskId": 9, "name": "data", "type": "SSD", "size": 20, "bootDisk": false}})
		default:
			t.Errorf("unexpected %s %s", q.Method, q.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	config := fmt.Sprintf(`provider "prodata" {
 api_base_url=%q
 api_key_id="fake"
 api_secret_key="fake"
 region="TEST"
 project_tag="project"
 }
 resource "prodata_volume_attachment" "data" {
 vm_id=77
 volume_id=9
 }
 `, srv.URL)
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"prodata": providerserver.NewProtocol6WithError(&volumeMoveTestProvider{})}, Steps: []resource.TestStep{
		{Config: config, Check: resource.TestCheckResourceAttr("prodata_volume_attachment.data", "attached_volume_id", "515"), ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		{Config: config, PlanOnly: true, PreConfig: func() { mu.Lock(); defer mu.Unlock(); status = "UNVERIFIED" }},
		{ResourceName: "prodata_volume_attachment.data", ImportState: true, ImportStateId: "77:9", ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "attached_volume_id", ImportStateVerifyIgnore: []string{"status"}, PreConfig: func() { mu.Lock(); defer mu.Unlock(); status = "SUCCESS" }},
	}})
	mu.Lock()
	defer mu.Unlock()
	if reserves != 1 || submits != 1 || moves != 1 || deletes != 1 || exists {
		t.Fatalf("reserves=%d submits=%d physicalMoves=%d detaches=%d exists=%v", reserves, submits, moves, deletes, exists)
	}
}
