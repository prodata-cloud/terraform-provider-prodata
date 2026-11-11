package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"sync"
	"testing"

	"terraform-provider-prodata/internal/client"
	"terraform-provider-prodata/internal/provider/resources"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	frameworkresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Keep this factory independent of the production registry while the epic's
// isolated branches are combined. Every request is confined to httptest's loopback server.
type securityGroupLocalProvider struct{ ProDataProvider }

func (p *securityGroupLocalProvider) Resources(_ context.Context) []func() frameworkresource.Resource {
	return []func() frameworkresource.Resource{resources.NewSecurityGroupResource, resources.NewSecurityGroupAttachmentResource}
}

func TestSecurityGroupsLocalTerraformLifecycle(t *testing.T) {
	terraformPath, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("Terraform CLI is not installed")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", terraformPath)
	var mu sync.Mutex
	var group *client.SecurityGroup
	attached := false
	creates, updates, attaches, detaches := 0, 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if req.Header.Get("X-Project-Tag") != "chosen" || req.Header.Get("X-Region") != "TEST" {
			t.Errorf("incorrect scope: %s %s", req.Header.Get("X-Project-Tag"), req.Header.Get("X-Region"))
		}
		answer := func(data any) { _ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data}) }
		absent := func() {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []any{map[string]any{"code": 703, "message": "not found"}}})
		}
		const base = "/panel-main/api/v2/security-groups"
		switch req.Method + " " + req.URL.Path {
		case "POST " + base:
			if group != nil {
				t.Error("recreated group instead of updating in place")
			}
			var body client.SecurityGroupRequest
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			group = &client.SecurityGroup{ID: 7, Name: body.Name, Rules: body.Rules}
			creates++
			answer(group)
		case "GET " + base + "/7":
			if group == nil {
				absent()
				return
			}
			answer(group)
		case "PUT " + base + "/7":
			if group == nil {
				absent()
				return
			}
			var body client.SecurityGroupRequest
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			group.Name, group.Rules = body.Name, body.Rules
			updates++
			answer(group)
		case "DELETE " + base + "/7":
			if attached {
				t.Error("deleted group before detaching its VM interface")
			}
			group = nil
			answer(nil)
		case "PUT " + base + "/7/attachments/8/9":
			if group == nil || len(group.Rules) == 0 {
				t.Error("attached a missing or empty group")
			}
			attached = true
			attaches++
			answer(client.SecurityGroupAttachment{GroupID: 7, VmID: 8, InterfaceID: 9})
		case "GET " + base + "/7/attachments/8/9":
			if !attached {
				absent()
				return
			}
			answer(client.SecurityGroupAttachment{GroupID: 7, VmID: 8, InterfaceID: 9})
		case "DELETE " + base + "/7/attachments/8/9":
			attached = false
			detaches++
			answer(nil)
		default:
			t.Errorf("unexpected HTTP request: %s %s", req.Method, req.URL.Path)
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(server.Close)
	config := func(name, port string) string {
		return fmt.Sprintf(`
provider "prodata" {
  api_base_url = %q
  api_key_id = "fake-key"
  api_secret_key = "fake-secret"
  region = "TEST"
  project_tag = "chosen"
}
resource "prodata_security_group" "test" {
  name = %q
  rules = [
    { direction = "in", action = "ACCEPT", protocol = "tcp", destination_port = %q },
    { direction = "in", action = "DROP" },
    { direction = "out", action = "ACCEPT" }
  ]
}
resource "prodata_security_group_attachment" "test" {
  security_group_id = prodata_security_group.test.id
  vm_id = 8
  interface_id = 9
}
`, server.URL, name, port)
	}
	stable := resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"prodata": providerserver.NewProtocol6WithError(&securityGroupLocalProvider{ProDataProvider{version: "test"}}),
		},
		CheckDestroy: func(_ *terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if group != nil || attached {
				return fmt.Errorf("group/binding survived destroy")
			}
			if creates != 1 || updates != 2 || attaches != 1 || detaches != 1 {
				return fmt.Errorf("unexpected calls: create=%d update=%d attach=%d detach=%d", creates, updates, attaches, detaches)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config("web", "443"), ConfigPlanChecks: stable, Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("prodata_security_group.test", "id", "7"),
				resource.TestCheckResourceAttr("prodata_security_group.test", "rules.0.destination_port", "443"),
				resource.TestCheckResourceAttr("prodata_security_group.test", "rules.1.action", "DROP"),
				resource.TestCheckResourceAttr("prodata_security_group_attachment.test", "id", "7/8/9"))},
			{Config: config("renamed", "8443"), ConfigPlanChecks: stable},
			{ResourceName: "prodata_security_group.test", ImportState: true, ImportStateId: "7", ImportStateVerify: true},
			{ResourceName: "prodata_security_group_attachment.test", ImportState: true, ImportStateId: "7/8/9", ImportStateVerify: true},
			{PreConfig: func() { mu.Lock(); defer mu.Unlock(); group.Rules[0].DestinationPort = "9999" }, Config: config("renamed", "8443"), ConfigPlanChecks: stable},
		},
	})
}
