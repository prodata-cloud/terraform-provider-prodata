package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"terraform-provider-prodata/internal/client"
)

// Actual Terraform must persist request_uuid even when no canonical ID was acknowledged.
func TestVolumeCreationLocalTerraformNullableIdCheckpointAndPositiveCancel(t *testing.T) {
	tf, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("local Terraform binary not installed")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", tf)
	t.Setenv("TF_ACC_TERRAFORM_VERSION", "")
	var mu sync.Mutex
	readable := false
	firstUUID := ""
	ids := map[string]int64{}
	states := map[string]string{}
	reserves, cancelled, physicalCreates, physicalDeletes := 0, 0, 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		answer := func(v any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": v})
		}
		if q.Header.Get("X-Region") != "TEST" || q.Header.Get("X-Project-Tag") != "test" {
			t.Error("checkpoint lost its scope")
		}
		prefix := "/panel-main/api/v2/volumes/by-user-disk/creation/"
		if strings.HasPrefix(q.URL.Path, prefix) {
			suffix := strings.TrimPrefix(q.URL.Path, prefix)
			if suffix == "capabilities" {
				answer(map[string]any{"contract": client.VolumeCreationContract, "reserveBeforeSubmit": true, "requestLookup": true, "cancelTombstone": true, "canonicalIdentity": "user-disk-v1"})
				return
			}
			uuid := strings.TrimSuffix(suffix, "/submit")
			view := func() {
				answer(map[string]any{"contract": client.VolumeCreationContract, "requestUuid": uuid, "volumeId": ids[uuid], "region": "TEST", "projectTag": "test", "name": "data", "type": "SSD", "size": 10, "status": states[uuid], "exists": states[uuid] != "CANCELLED", "canRelease": states[uuid] == "CANCELLED"})
			}
			if q.Method == http.MethodPost && !strings.HasSuffix(suffix, "/submit") {
				if _, known := ids[uuid]; !known {
					for old, state := range states {
						if state != "CANCELLED" && state != "DELETED" {
							t.Errorf("new UUID %s tried to allocate while %s remained %s", uuid, old, state)
							w.WriteHeader(409)
							return
						}
					}
					reserves++
					ids[uuid] = int64(8 + reserves)
					states[uuid] = "RESERVED"
					if firstUUID == "" {
						firstUUID = uuid
					}
				}
				if !readable {
					w.WriteHeader(503)
					return
				}
				view()
				return
			}
			if q.Method == http.MethodGet {
				if !readable {
					w.WriteHeader(503)
					return
				}
				view()
				return
			}
			if q.Method == http.MethodDelete {
				if states[uuid] == "RESERVED" {
					states[uuid] = "CANCELLED"
					cancelled++
				}
				view()
				return
			}
			if q.Method == http.MethodPost && strings.HasSuffix(suffix, "/submit") {
				if states[uuid] != "RESERVED" {
					t.Error("an unrelated or completed reservation was reactivated")
					w.WriteHeader(409)
					return
				}
				states[uuid] = "READY"
				physicalCreates++
				view()
				return
			}
		}
		if strings.HasPrefix(q.URL.Path, "/panel-main/api/v2/volumes/by-user-disk/") {
			var id int64
			_, _ = fmt.Sscanf(strings.TrimPrefix(q.URL.Path, "/panel-main/api/v2/volumes/by-user-disk/"), "%d", &id)
			for uuid, known := range ids {
				if known == id {
					if q.Method == http.MethodDelete {
						if states[uuid] != "READY" {
							t.Error("unverified disk reached physical deletion")
						}
						states[uuid] = "DELETED"
						physicalDeletes++
					}
					exists := states[uuid] == "READY"
					answer(map[string]any{"identityVersion": "user-disk-v1", "id": id, "exists": exists, "name": "data", "type": "SSD", "size": 10, "inUse": false, "creationRequestUuid": uuid})
					return
				}
			}
		}
		t.Errorf("unexpected %s %s", q.Method, q.URL.Path)
		w.WriteHeader(500)
	}))
	defer srv.Close()
	config := fmt.Sprintf(`provider "prodata" {
 api_base_url=%q
 api_key_id="fixture"
 api_secret_key="fixture"
 region="TEST"
 project_tag="test"
 }
 resource "prodata_volume" "data" {
 name="data"
 type="SSD"
 size=10
 }
 `, srv.URL)
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories, Steps: []resource.TestStep{
		{Config: config, ExpectError: regexp.MustCompile("Volume Reservation Unverified")},
		{Config: config, PreConfig: func() { mu.Lock(); readable = true; mu.Unlock() }, ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
	}})
	mu.Lock()
	defer mu.Unlock()
	if firstUUID == "" || reserves != 2 || states[firstUUID] != "CANCELLED" || cancelled != 1 || physicalCreates != 1 || physicalDeletes != 1 {
		t.Fatalf("nullable ID checkpoint lost: first=%q states=%v reserves=%d cancelled=%d physical=%d/%d", firstUUID, states, reserves, cancelled, physicalCreates, physicalDeletes)
	}
}
