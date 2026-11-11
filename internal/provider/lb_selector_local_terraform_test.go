package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"regexp"
	"sort"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// The real API preserves ASG declarations for legacy API-key configure calls.
// A configure must opt in to replacement; otherwise the next sync resurrects old members.
func TestLbSelectorLocalTerraformReplacesDeclarationsAndRejectsOldServer(t *testing.T) {
	cli, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("Terraform CLI is not installed")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", cli)
	var mu sync.Mutex
	groups := map[int64]bool{}
	manual := ""
	created, deleted, configures := 0, 0, 0
	supported := true
	extraneousPool := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if req.Header.Get("X-Project-Tag") != "chosen" || req.Header.Get("X-Region") != "TEST" {
			t.Error("lost stored LB scope")
		}
		switch req.Method + " " + req.URL.Path {
		case "POST /panel-main/api/loadbalancer/createLoadbalancer", "POST /panel-main/api/loadbalancer/configureLoadbalancer/42":
			var body struct {
				Replace  bool `json:"replaceBackendSelectors"`
				Backends []struct {
					ASGID int64  `json:"asgId"`
					VM    string `json:"userVmId"`
				} `json:"backends"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if len(body.Backends) != 1 {
				t.Errorf("expected one complete selector: %+v", body)
			}
			if req.URL.Path == "/panel-main/api/loadbalancer/createLoadbalancer" {
				created++
			} else {
				configures++
				// The flag is ignored by an old server. New server semantics are explicitly opt-in.
				if supported && body.Replace {
					groups = map[int64]bool{}
					extraneousPool = false
				}
			}
			manual = ""
			for _, backend := range body.Backends {
				if backend.ASGID > 0 {
					groups[backend.ASGID] = true
				} else {
					manual = backend.VM
				}
			}
		case "GET /panel-main/api/loadbalancer/getLoadBalancer":
			if req.URL.Query().Get("loadBalancerId") != "42" {
				t.Error("wrong LB identity")
			}
		case "POST /panel-main/api/loadbalancer/deleteLoadbalancer/42":
			deleted++
		default:
			t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
			http.NotFound(w, req)
			return
		}
		// Simulate the next sync tick: every still-live declaration adds its roster back.
		declarations := []any{}
		backends := []any{}
		ids := []int{}
		for id := range groups {
			ids = append(ids, int(id))
		}
		sort.Ints(ids)
		for _, id := range ids {
			declarations = append(declarations, map[string]any{"kind": "ASG", "asgId": id})
			backends = append(backends, map[string]any{"userVM": map[string]any{"guid": fmt.Sprintf("asg-%d-member", id)}, "managedByGroup": true, "status": map[string]any{"name": "SUCCESS"}})
		}
		// An out-of-band zero-member pool has no VM identifiers to expose as drift.
		if extraneousPool {
			declarations = append(declarations, map[string]any{"kind": "NODE_POOL", "nodePoolId": 99})
		}
		if manual != "" {
			backends = append(backends, map[string]any{"userVM": map[string]any{"guid": manual}, "managedByGroup": false, "status": map[string]any{"name": "SUCCESS"}})
		}
		status := "SUCCESS"
		if deleted > 0 {
			status = "DELETED"
		}
		data := map[string]any{"id": 42, "name": "replace-lb", "description": "desc", "source": "FRONTEND", "userNet": 9, "protocol": "TCP", "isPublic": false, "status": map[string]any{"name": status}, "ports": []any{map[string]any{"srcPort": 80, "dstPort": 8080}}, "backends": backends, "backendGroups": declarations, "dateCreated": "2026-11-06T00:00:00Z"}
		if supported {
			data["backendSelectorContract"] = "replace-v1"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": data})
	}))
	t.Cleanup(server.Close)
	config := func(selector string) string {
		return fmt.Sprintf(`
provider "prodata" {
  api_base_url = %q
  api_key_id = "fake-key"
  api_secret_key = "fake-secret"
  region = "TEST"
  project_tag = "chosen"
}
resource "prodata_lb" "test" {
  name = "replace-lb"
  description = "desc"
  type = "internal"
  protocol = "TCP"
  network_id = 9
  backend_group = { %s }
  port = [{ port = 80, target_port = 8080 }]
}
`, server.URL, selector)
	}
	stable := resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	check := func(asg int64, vm string) resource.TestCheckFunc {
		return func(_ *terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if asg > 0 && (len(groups) != 1 || !groups[asg] || manual != "") || asg == 0 && (len(groups) != 0 || manual != vm) {
				return fmt.Errorf("stale declarations/traffic: groups=%v manual=%q", groups, manual)
			}
			return nil
		}
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(_ *terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if created != 1 || deleted != 1 || configures != 5 {
				return fmt.Errorf("LB replaced, old server mutated, or update missing: create=%d delete=%d configure=%d", created, deleted, configures)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config("asg_id = 7"), ConfigPlanChecks: stable, Check: check(7, "")},
			{PreConfig: func() { mu.Lock(); defer mu.Unlock(); supported = false }, Config: config("asg_id = 8"), ExpectError: regexp.MustCompile("backend selector|selector contract")},
			{PreConfig: func() {
				mu.Lock()
				defer mu.Unlock()
				if configures != 0 {
					t.Errorf("old server received %d mutations", configures)
				}
				supported = true
			}, Config: config("asg_id = 8"), ConfigPlanChecks: stable, Check: check(8, "")},
			{Config: config(`vm_ids = ["manual-vm"]`), ConfigPlanChecks: stable, Check: check(0, "manual-vm")},
			{PreConfig: func() { mu.Lock(); defer mu.Unlock(); extraneousPool = true }, Config: config(`vm_ids = ["manual-vm"]`), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("prodata_lb.test", plancheck.ResourceActionUpdate)}, PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}, Check: check(0, "manual-vm")},
			{Config: config("asg_id = 7"), ConfigPlanChecks: stable, Check: check(7, "")},
			{PreConfig: func() { mu.Lock(); defer mu.Unlock(); manual = "out-of-band-vm" }, Config: config("asg_id = 7"), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("prodata_lb.test", plancheck.ResourceActionUpdate)}, PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}, Check: check(7, "")},
		},
	})
}
