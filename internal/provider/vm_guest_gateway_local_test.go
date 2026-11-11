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
	"terraform-provider-prodata/internal/testutil"
	"testing"
)

type guestGatewayTestProvider struct{ ProDataProvider }

func (p *guestGatewayTestProvider) Resources(context.Context) []func() frameworkresource.Resource {
	return []func() frameworkresource.Resource{resources.NewVmGatewayResource}
}
func TestGuestGatewayLocalTerraformLostAckAndUnavailableReadKeepEmptyPlan(t *testing.T) {
	tf, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("local Terraform binary not installed")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", tf)
	t.Setenv("TF_ACC_TERRAFORM_VERSION", "")
	var mu sync.Mutex
	vmStatus := "RUNNING"
	var gateway *string
	uuid := ""
	guestStatus := ""
	configured, unavailable, childVerified := false, false, false
	sets, clears, guestExecs, stops, starts := 0, 0, 0, 0, 0
	server := httptest.NewServer(testutil.GatewayJournal(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		ok := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": v}) }
		guest := func(status string, verified bool) map[string]any {
			return map[string]any{"contract": client.GuestNetworkContract, "recoverableSubmission": true, "recoverablePowerChildren": true, "supported": !unavailable, "configured": configured && !unavailable, "driver": "netplan-v1", "operationUuid": uuid, "status": status, "pending": status == "RESERVED", "verified": verified && !unavailable, "cloudInitDisabled": false, "released": false, "reason": "guest agent temporarily unavailable"}
		}
		p := strings.TrimPrefix(q.URL.Path, "/panel-main/api/v2/vms/7/")
		switch {
		case p == "power-operations/capabilities":
			ok(map[string]any{"contract": client.VmPowerOperationContract, "callerUuid": true, "claimBeforeWrite": true, "readNeverActivates": true, "resolveTombstone": true, "recoverablePowerChildren": true, "stopCheckpoint": true, "supportedActions": []string{"START", "REBOOT", "RESET", "RESUME", "STOP", "SHUTDOWN"}})
		case p == "status":
			ok(map[string]any{"id": 7, "status": vmStatus})
		case p == "stop":
			stops++
			vmStatus = "STOPPED"
			ok(nil)
		case p == "start":
			starts++
			vmStatus = "RUNNING"
			ok(nil)
		case p == "local-networks":
			ok([]map[string]any{{"id": 91, "networkId": 42, "ip": "192.0.2.5", "mac": "BC:24:11:AA:BB:CC"}})
		case p == "guest-network-capabilities":
			v := guest("", true)
			v["supported"] = true
			v["pending"] = false
			ok(v)
		case p == "local-networks/91/guest-configuration/dry-run":
			v := guest("", true)
			v["pending"] = false
			ok(v)
		case p == "local-networks/91/guest-configuration/reservations":
			var r client.ConfigureGuestNetworkRequest
			_ = json.NewDecoder(q.Body).Decode(&r)
			uuid = "11111111-1111-4111-8111-111111111111"
			if r.Gateway == nil {
				uuid = "22222222-2222-4222-8222-222222222222"
			}
			guestStatus = "RESERVED"
			ok(guest(guestStatus, false))
		case p == "local-networks/91/guest-configuration":
			if q.Method == "POST" {
				guestExecs++
				configured = true
				guestStatus = "SUCCESS"
				if guestExecs == 1 {
					w.WriteHeader(502)
					return
				}
			}
			ok(guest(guestStatus, true))
		case p == "gateway" && q.Method == "GET":
			ok(map[string]any{"interfaceId": 91, "networkId": 42, "gateway": gateway, "present": gateway != nil, "pending": false, "reconcileRequired": false, "otherGatewayActive": false, "requiresGuestConfiguration": false, "guestOperationUuid": uuid, "guestChildVerified": childVerified && gateway != nil})
		case p == "gateway":
			var r client.ChangeVmGatewayRequest
			_ = json.NewDecoder(q.Body).Decode(&r)
			changed := gateway == nil || r.Gateway == nil || *gateway != *r.Gateway
			if r.Clear {
				changed = gateway != nil && r.ExpectedGateway != nil && *gateway == *r.ExpectedGateway
			}
			answer := r.Gateway
			if r.Clear && !changed {
				answer = gateway
			}
			if q.URL.Query().Get("dryRun") != "true" {
				if q.Header.Get("X-Prodata-Guest-Operation-UUID") != uuid || vmStatus != "STOPPED" {
					t.Errorf("unreserved physical gateway mutation uuid=%q status=%s", q.Header.Get("X-Prodata-Guest-Operation-UUID"), vmStatus)
				}
				gateway = answer
				childVerified = true
				if r.Clear {
					clears++
				} else {
					sets++
				}
			}
			ok(map[string]any{"interfaceId": 91, "networkId": 42, "gateway": answer, "changed": changed, "previousGatewayOnAnotherInterface": false, "requiresGuestConfiguration": false})
		default:
			t.Errorf("unexpected %s %s", q.Method, q.URL.Path)
			w.WriteHeader(500)
		}
	})))
	defer server.Close()
	config := fmt.Sprintf(`provider "prodata" {
 api_base_url=%q
 api_key_id="fake"
 api_secret_key="fake"
 region="TEST"
 project_tag="project"
 }
 resource "prodata_vm_gateway" "guest" {
 vm_id=7
 network_id=42
 gateway="192.0.2.1"
 configure_guest=true
 }`, server.URL)
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"prodata": providerserver.NewProtocol6WithError(&guestGatewayTestProvider{})}, Steps: []resource.TestStep{
		{Config: config, Check: resource.TestCheckResourceAttr("prodata_vm_gateway.guest", "guest_configuration_applied", "true"), ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		{Config: config, PlanOnly: true, PreConfig: func() { mu.Lock(); defer mu.Unlock(); unavailable = true }},
		{Config: config, PlanOnly: true, PreConfig: func() { mu.Lock(); defer mu.Unlock(); unavailable = false }},
	}})
	mu.Lock()
	defer mu.Unlock()
	if sets != 1 || clears != 1 || guestExecs != 2 || stops != 2 || starts != 2 || gateway != nil {
		t.Fatalf("set=%d clear=%d guest=%d stop=%d start=%d gateway=%v", sets, clears, guestExecs, stops, starts, gateway)
	}
}
