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

// This uses the real Terraform CLI and an in-process provider against a loopback-only fake panel.
// It checks Terraform's plan/state consistency as well as the conditional-delete safety rule.
func TestVmGatewayLocalTerraformLifecycle(t *testing.T) {
	if _, err := exec.LookPath("terraform"); err != nil {
		t.Skip("Terraform CLI is not installed")
	}
	var mu sync.Mutex
	var gateway *string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		answer := func(data any) { _ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data}) }
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/panel-main/api/v2/vms/7/status":
			answer(map[string]any{"id": 7, "status": "STOPPED"})
		case req.Method == http.MethodGet && req.URL.Path == "/panel-main/api/v2/vms/7/gateway":
			answer(map[string]any{"interfaceId": 91, "networkId": 42, "gateway": gateway,
				"present": gateway != nil, "pending": false, "reconcileRequired": false,
				"otherGatewayActive": false, "requiresGuestConfiguration": false})
		case req.Method == http.MethodPut && req.URL.Path == "/panel-main/api/v2/vms/7/gateway":
			var change client.ChangeVmGatewayRequest
			if err := json.NewDecoder(req.Body).Decode(&change); err != nil {
				t.Errorf("decode request: %v", err)
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			if change.NetworkID != 42 || (change.ExpectedInterfaceID != nil && *change.ExpectedInterfaceID != 91) {
				t.Errorf("unexpected target: %+v", change)
				http.Error(w, "invalid target", http.StatusBadRequest)
				return
			}
			changed := false
			next := gateway
			if change.Clear {
				changed = gateway != nil && change.ExpectedGateway != nil && *gateway == *change.ExpectedGateway
				if changed {
					next = nil
				}
			} else if change.Gateway != nil {
				changed = gateway == nil || *gateway != *change.Gateway
				next = change.Gateway
			} else {
				t.Error("missing gateway")
				http.Error(w, "invalid gateway", http.StatusBadRequest)
				return
			}
			if req.URL.Query().Get("dryRun") != "true" {
				gateway = next
			}
			answer(map[string]any{"interfaceId": 91, "networkId": 42, "gateway": next,
				"changed": changed, "previousGatewayOnAnotherInterface": false,
				"requiresGuestConfiguration": false})
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(server.Close)

	config := func(value string) string {
		return fmt.Sprintf(`
provider "prodata" {
  api_base_url = %q
  api_key_id = "fake-key"
  api_secret_key = "fake-secret"
  region = "TEST"
  project_tag = "test"
}
resource "prodata_vm_gateway" "test" {
  vm_id = 7
  network_id = 42
  gateway = %q
}`, server.URL, value)
	}
	setForeign := func() {
		mu.Lock()
		defer mu.Unlock()
		foreign := "192.0.2.99"
		gateway = &foreign
	}
	checkState := func(value string) []statecheck.StateCheck {
		return []statecheck.StateCheck{
			statecheck.ExpectKnownValue("prodata_vm_gateway.test", tfjsonpath.New("gateway"), knownvalue.StringExact(value)),
			statecheck.ExpectKnownValue("prodata_vm_gateway.test", tfjsonpath.New("owned_gateway"), knownvalue.StringExact(value)),
			statecheck.ExpectKnownValue("prodata_vm_gateway.test", tfjsonpath.New("interface_id"), knownvalue.Int64Exact(91)),
		}
	}
	stable := resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(_ *terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if gateway == nil || *gateway != "192.0.2.99" {
				return fmt.Errorf("destroy removed the route set by another actor: %v", gateway)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config("192.0.2.1"), ConfigStateChecks: checkState("192.0.2.1"), ConfigPlanChecks: stable},
			{Config: config("192.0.2.2"), ConfigStateChecks: checkState("192.0.2.2"), ConfigPlanChecks: stable},
			{ResourceName: "prodata_vm_gateway.test", ImportState: true, ImportStateId: "7:42", ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "vm_id"},
			{PreConfig: setForeign, Config: config("192.0.2.2"), ConfigStateChecks: checkState("192.0.2.2"), ConfigPlanChecks: stable},
			{PreConfig: setForeign, Config: config("192.0.2.2"), Destroy: true},
		},
	})
}
