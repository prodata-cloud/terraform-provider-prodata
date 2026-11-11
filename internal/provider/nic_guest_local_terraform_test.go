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
	"terraform-provider-prodata/internal/testutil"
	"testing"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Real Terraform, loopback API only: gateway-address opt-in, guest configuration,
// drift reconciliation without NIC replacement, and conditional guest-route cleanup.
func TestNicGuestLocalTerraformLifecycle(t *testing.T) {
	path, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("Terraform CLI is not installed")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", path)
	var mu sync.Mutex
	nicExists, guestAddress := false, false
	var gateway, guestGateway *string
	vmStatus := "RUNNING"
	attaches, detaches, guestWrites := 0, 0, 0
	nicStatus := "RESERVED"
	guestUUID, guestStatus := "", "SUCCESS"
	uuidCounter := 0
	allocateGuestUUID := func() { uuidCounter++; guestUUID = fmt.Sprintf("%08x-1111-4111-8111-111111111111", uuidCounter) }
	ptr := func(value string) *string { return &value }
	server := httptest.NewServer(testutil.GatewayJournal(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if req.Header.Get("X-Project-Tag") != "chosen" || req.Header.Get("X-Region") != "TEST" {
			t.Error("lost explicit provider scope")
		}
		ok := func(data any) { _ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data}) }
		guest := func(configured bool) {
			ok(map[string]any{"supported": true, "configured": configured, "driver": "netplan-v1", "reason": nil, "contract": client.GuestNetworkContract, "recoverableSubmission": true, "recoverablePowerChildren": true, "operationUuid": guestUUID, "status": guestStatus, "pending": guestStatus == "RESERVED", "verified": guestStatus != "RESERVED", "cloudInitDisabled": false, "released": false})
		}
		nic := func() map[string]any {
			return map[string]any{"id": 91, "networkId": 42, "networkName": "local", "ip": "192.0.2.1", "mac": "BC:24:11:AA:BB:CC", "gateway": gateway, "primary": gateway != nil}
		}
		const base = "/panel-main/api/v2/vms/7"
		localView := func() {
			v := nic()
			v["contract"] = client.LocalNetworkAttachmentContract
			v["vmId"] = 7
			v["exists"] = nicExists
			v["status"] = nicStatus
			v["canRelease"] = false
			v["operation"] = "ATTACH_LOCAL_NIC"
			if !nicExists {
				v["operation"] = "DETACH_LOCAL_NIC"
			}
			v["netIndex"] = 1
			v["pveFullName"] = "net1"
			v["requiresRestart"] = false
			v["requiresGuestConfiguration"] = true
			ok(v)
		}
		switch req.Method + " " + req.URL.Path {
		case "GET " + base + "/power-operations/capabilities":
			ok(map[string]any{"contract": client.VmPowerOperationContract, "callerUuid": true, "claimBeforeWrite": true, "readNeverActivates": true, "resolveTombstone": true, "recoverablePowerChildren": true, "stopCheckpoint": true, "supportedActions": []string{"START", "REBOOT", "RESET", "RESUME", "STOP", "SHUTDOWN"}})
		case "GET " + base + "/guest-network-capabilities":
			if vmStatus != "RUNNING" {
				t.Error("guest preflight on a stopped VM")
			}
			ok(map[string]any{"supported": true, "configured": false, "driver": "netplan-v1", "contract": client.GuestNetworkContract, "recoverableSubmission": true, "recoverablePowerChildren": true, "pending": false, "verified": true, "cloudInitDisabled": false})
		case "GET " + base + "/local-network-attachments/capabilities":
			ok(map[string]any{"contract": client.LocalNetworkAttachmentContract, "reserveBeforeSubmit": true, "recoverableSubmission": true, "strictInterfaceIdentity": true})
		case "POST " + base + "/local-network-attachments/reservations":
			var body client.AttachLocalNetworkRequest
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if !body.AllowGatewayAddress || body.IP != "192.0.2.1" || body.NetworkID != 42 {
				t.Errorf("gateway-address opt-in missing: %+v", body)
			}
			if nicExists {
				t.Error("NIC replaced during a guest-only update")
			}
			nicExists = true
			nicStatus = "RESERVED"
			localView()
		case "POST " + base + "/local-network-attachments/91/submit":
			attaches++
			nicStatus = "SUCCESS"
			localView()
		case "GET " + base + "/local-network-attachments/91":
			localView()
		case "GET " + base + "/local-networks":
			data := []any{}
			if nicExists {
				data = append(data, nic())
			}
			ok(data)
		case "DELETE " + base + "/local-network-attachments/91":
			if gateway != nil {
				t.Error("NIC detached before platform gateway removal")
			}
			nicExists = false
			detaches++
			nicStatus = "DELETED"
			localView()
		case "POST " + base + "/local-networks/91/guest-configuration/dry-run":
			ok(map[string]any{"supported": true, "configured": false, "driver": "netplan-v1", "contract": client.GuestNetworkContract, "recoverableSubmission": true, "recoverablePowerChildren": true, "pending": false, "verified": true, "cloudInitDisabled": false})
		case "POST " + base + "/local-networks/91/guest-configuration/reservations":
			allocateGuestUUID()
			guestStatus = "RESERVED"
			guest(false)
		case "DELETE " + base + "/local-networks/91/guest-configuration":
			allocateGuestUUID()
			guestStatus = "SUCCESS"
			v := map[string]any{"supported": true, "configured": false, "driver": "netplan-v1", "contract": client.GuestNetworkContract, "recoverableSubmission": true, "recoverablePowerChildren": true, "operationUuid": guestUUID, "status": guestStatus, "pending": false, "verified": true, "cloudInitDisabled": false, "released": true}
			ok(v)
		case "GET " + base + "/local-networks/91/guest-configuration":
			if req.URL.Query().Get("expectedMac") != "BC:24:11:AA:BB:CC" || req.URL.Query().Get("expectedIp") != "192.0.2.1" {
				t.Error("guest state lookup has no stable identity")
			}
			configured := guestAddress
			if req.URL.Query().Get("configureDefaultRoute") == "true" {
				desired := req.URL.Query().Get("gateway")
				expected := req.URL.Query().Get("expectedGuestGateway")
				if desired != "" {
					configured = configured && guestGateway != nil && *guestGateway == desired
				} else {
					configured = configured && (guestGateway == nil || expected != "" && *guestGateway != expected)
				}
			}
			guest(configured)
		case "POST " + base + "/local-networks/91/guest-configuration":
			var body client.ConfigureGuestNetworkRequest
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if vmStatus != "RUNNING" || !nicExists || body.ExpectedMAC != "BC:24:11:AA:BB:CC" || body.ExpectedIP != "192.0.2.1" {
				t.Error("guest configuration has wrong target or VM state")
			}
			if guestStatus != "RESERVED" {
				allocateGuestUUID()
			}
			guestStatus = "SUCCESS"
			guestAddress = true
			guestWrites++
			if body.ConfigureDefaultRoute {
				if body.Gateway != nil {
					if gateway == nil || *gateway != *body.Gateway {
						t.Error("guest default gateway not verified against platform")
					}
					guestGateway = ptr(*body.Gateway)
				} else if body.ExpectedGuestGateway != nil && guestGateway != nil && *guestGateway == *body.ExpectedGuestGateway {
					guestGateway = nil
				}
			}
			guest(true)
		case "GET " + base + "/gateway":
			ok(map[string]any{"interfaceId": 91, "networkId": 42, "gateway": gateway, "present": gateway != nil, "pending": false, "reconcileRequired": false, "otherGatewayActive": false, "requiresGuestConfiguration": true})
		case "PUT " + base + "/gateway":
			var body client.ChangeVmGatewayRequest
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			next := gateway
			changed := false
			if body.Clear {
				changed = gateway != nil && body.ExpectedGateway != nil && *gateway == *body.ExpectedGateway
				if changed {
					next = nil
				}
			} else if body.Gateway != nil {
				changed = gateway == nil || *gateway != *body.Gateway
				next = body.Gateway
			}
			if req.URL.Query().Get("dryRun") != "true" {
				if vmStatus != "STOPPED" {
					t.Error("gateway changed without stopping running VM")
				}
				gateway = next
			}
			ok(map[string]any{"interfaceId": 91, "networkId": 42, "gateway": next, "changed": changed, "previousGatewayOnAnotherInterface": false, "requiresGuestConfiguration": true})
		case "GET " + base + "/status":
			ok(map[string]any{"id": 7, "status": vmStatus})
		case "POST " + base + "/stop":
			vmStatus = "STOPPED"
			ok(nil)
		case "POST " + base + "/start":
			vmStatus = "RUNNING"
			ok(nil)
		default:
			t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
			http.NotFound(w, req)
		}
	})))
	t.Cleanup(server.Close)
	config := fmt.Sprintf(`
provider "prodata" {
  api_base_url = %q
  api_key_id = "fake-key"
  api_secret_key = "fake-secret"
  region = "TEST"
  project_tag = "chosen"
}
resource "prodata_local_network_attachment" "test" {
  vm_id = 7
  network_id = 42
  ip = "192.0.2.1"
  allow_gateway_address = true
  configure_guest = true
  reboot_after_attach = false
}
resource "prodata_vm_gateway" "test" {
  vm_id = 7
  network_id = 42
  gateway = "192.0.2.254"
  configure_guest = true
  depends_on = [prodata_local_network_attachment.test]
}
`, server.URL)
	stable := resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(_ *terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if nicExists || gateway != nil || attaches != 1 || detaches != 1 {
				return fmt.Errorf("unexpected attachment lifecycle: exists=%v attaches=%d detaches=%d gateway=%v", nicExists, attaches, detaches, gateway)
			}
			if guestWrites < 4 {
				return fmt.Errorf("guest drift was not reconciled: writes=%d", guestWrites)
			}
			if guestGateway == nil || *guestGateway != "192.0.2.253" {
				return fmt.Errorf("conditional cleanup removed a foreign guest route")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config, ConfigPlanChecks: stable, Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("prodata_local_network_attachment.test", "guest_configuration_applied", "true"),
				resource.TestCheckResourceAttr("prodata_local_network_attachment.test", "allow_gateway_address", "true"),
				resource.TestCheckResourceAttr("prodata_vm_gateway.test", "guest_configuration_applied", "true"))},
			{PreConfig: func() { mu.Lock(); defer mu.Unlock(); guestAddress = false; guestGateway = ptr("192.0.2.253") }, Config: config, ConfigPlanChecks: stable},
			{Config: strings.ReplaceAll(config, "configure_guest = true", "configure_guest = false"), ConfigPlanChecks: stable},
			{Config: config, ConfigPlanChecks: stable},
			{PreConfig: func() { mu.Lock(); defer mu.Unlock(); guestGateway = ptr("192.0.2.253") }, Config: config, Destroy: true},
		},
	})
}

func TestNicGuestUnsupportedPreflightCreatesNothing(t *testing.T) {
	path, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("Terraform CLI is not installed")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", path)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet || !strings.HasSuffix(req.URL.Path, "/guest-network-capabilities") {
			t.Errorf("unsupported guest caused a mutation: %s %s", req.Method, req.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"supported": false, "configured": false, "driver": "netplan-v1", "reason": "FreeBSD requires manual configuration"}})
	}))
	t.Cleanup(server.Close)
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories, Steps: []resource.TestStep{{
		Config: fmt.Sprintf(`provider "prodata" {
api_base_url=%q
api_key_id="fake"
api_secret_key="fake"
region="TEST"
project_tag="chosen"
}
resource "prodata_local_network_attachment" "test" {
vm_id=7
network_id=42
configure_guest=true
}`, server.URL),
		ExpectError: regexp.MustCompile("Guest Network Automation Unavailable"),
	}}})
}
