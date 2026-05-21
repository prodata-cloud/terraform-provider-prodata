package provider

import (
	"context"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func init() {
	resource.AddTestSweepers("prodata_lb", &resource.Sweeper{
		Name: "prodata_lb",
		F:    sweepLBs,
	})
}

// testAccPreCheckLb additionally requires a pre-existing network and backend VM.
// A load balancer needs a backend group, and provisioning a VM inline would make
// the test slow and couple it to the VM resource; referencing existing infra via
// env (the same approach the prior client live test used) keeps it focused.
func testAccPreCheckLb(t *testing.T) {
	t.Helper()
	testAccPreCheck(t)
	for _, k := range []string{"PRODATA_LB_TEST_NET_ID", "PRODATA_LB_TEST_VM_GUID"} {
		if os.Getenv(k) == "" {
			t.Fatalf("%s must be set for prodata_lb acceptance tests", k)
		}
	}
}

// TestAccLb_basic exercises the full prodata_lb lifecycle: create+read, an in-place
// update (description), a data source read-back, and an import round-trip. The
// empty-plan assertions guard the two normalization behaviors that would otherwise
// cause drift: protocol upper-casing and date_created preservation across updates.
// An internal LB is used so the test does not consume a public IP.
func TestAccLb_basic(t *testing.T) {
	name := accName()
	resourceName := "prodata_lb.test"
	dataName := "data.prodata_lb.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheckLb(t); testAccProdMutationGuard(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckLbDestroy,
		Steps: []resource.TestStep{
			{ // Create + Read.
				Config: testAccLbConfig(name, "acc test lb"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("name"), knownvalue.StringExact(name)),
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("type"), knownvalue.StringExact("internal")),
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("protocol"), knownvalue.StringExact("TCP")),
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("source"), knownvalue.StringExact(client.LbSourceFrontend)),
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("status"), knownvalue.StringExact(client.LbStatusSuccess)),
				},
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{ // Update: change description in place.
				Config: testAccLbConfig(name, "acc test lb updated"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("description"), knownvalue.StringExact("acc test lb updated")),
				},
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{ // Data source read-back by id.
				Config: testAccLbConfigWithData(name, "acc test lb updated"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(dataName, tfjsonpath.New("name"), knownvalue.StringExact(name)),
					statecheck.ExpectKnownValue(dataName, tfjsonpath.New("type"), knownvalue.StringExact("internal")),
					statecheck.ExpectKnownValue(dataName, tfjsonpath.New("protocol"), knownvalue.StringExact("TCP")),
				},
			},
			{ // Import round-trip (bare id falls back to provider region/project).
				ResourceName:            resourceName,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"timeouts"},
				ImportStateIdFunc:       lbImportID(resourceName),
			},
		},
	})
}

// TestAccLb_protocolValidation re-homes the deleted protocol-OneOf unit test at the
// canonical layer: it asserts our schema rejects a lowercase protocol at plan time
// through the real Terraform validation path (the server would otherwise silently
// downgrade unknown protocols to TCP). No infrastructure is created.
func TestAccLb_protocolValidation(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccLbConfigInvalidProtocol(accName()),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?i)must be one of`),
			},
		},
	})
}

func testAccLbConfig(name, description string) string {
	return fmt.Sprintf(`
resource "prodata_lb" "test" {
  name        = %[1]q
  description = %[2]q
  type        = "internal"
  protocol    = "TCP"
  network_id  = %[3]s

  port = [
    { port = 80, target_port = 8080 },
  ]

  backend_group = {
    vm_ids = [%[4]q]
  }
}
`, name, description, os.Getenv("PRODATA_LB_TEST_NET_ID"), os.Getenv("PRODATA_LB_TEST_VM_GUID"))
}

func testAccLbConfigWithData(name, description string) string {
	return testAccLbConfig(name, description) + `
data "prodata_lb" "test" {
  id = prodata_lb.test.id
}
`
}

// testAccLbConfigInvalidProtocol uses dummy backend values: validation fails on the
// protocol before any field reaches the API, so no real network/VM is needed.
func testAccLbConfigInvalidProtocol(name string) string {
	return fmt.Sprintf(`
resource "prodata_lb" "test" {
  name       = %[1]q
  type       = "internal"
  protocol   = "tcp"
  network_id = 1

  port = [
    { port = 80, target_port = 8080 },
  ]

  backend_group = {
    vm_ids = ["dummy-guid"]
  }
}
`, name)
}

// lbImportID returns the bare load balancer id from state.
func lbImportID(resourceName string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("resource %s not found in state", resourceName)
		}
		return rs.Primary.Attributes["id"], nil
	}
}

func testAccCheckLbDestroy(s *terraform.State) error {
	c, err := accClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "prodata_lb" {
			continue
		}
		id, err := strconv.ParseInt(rs.Primary.Attributes["id"], 10, 64)
		if err != nil {
			return fmt.Errorf("parse lb id %q: %w", rs.Primary.Attributes["id"], err)
		}
		opts := &client.RequestOpts{
			Region:     rs.Primary.Attributes["region"],
			ProjectTag: rs.Primary.Attributes["project_tag"],
		}
		lb, err := c.GetLoadBalancer(ctx, id, opts)
		if err != nil {
			if client.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("unexpected error checking destroyed lb %d: %w", id, err)
		}
		if lb.Status == client.LbStatusDeleted {
			continue
		}
		return fmt.Errorf("load balancer %d still exists after destroy (status %s)", id, lb.Status)
	}
	return nil
}

// sweepLBs deletes acceptance-test load balancers left behind by interrupted runs,
// routing the delete by source the same way the resource does.
func sweepLBs(_ string) error {
	c, err := accClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	lbs, err := c.ListLoadBalancers(ctx, nil)
	if err != nil {
		return fmt.Errorf("list load balancers: %w", err)
	}
	for _, lb := range lbs {
		if !strings.HasPrefix(lb.Name, accResourcePrefix) {
			continue
		}
		var derr error
		switch lb.Source {
		case client.LbSourceCCM:
			derr = c.DeleteLoadBalancerCCM(ctx, lb.ID, nil)
		default:
			derr = c.DeleteLoadBalancerFrontend(ctx, lb.ID, nil)
		}
		if derr != nil && !client.IsNotFound(derr) {
			log.Printf("[WARN] sweep: failed to delete load balancer %d (%q): %v", lb.ID, lb.Name, derr)
		}
	}
	return nil
}
