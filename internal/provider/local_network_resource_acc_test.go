package provider

import (
	"context"
	"fmt"
	"log"
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
	resource.AddTestSweepers("prodata_local_network", &resource.Sweeper{
		Name: "prodata_local_network",
		F:    sweepLocalNetworks,
	})
}

// TestAccLocalNetwork_basic exercises the create+read, an in-place rename, plan
// stability, and an import round-trip of prodata_local_network.
func TestAccLocalNetwork_basic(t *testing.T) {
	name := accName()
	resourceName := "prodata_local_network.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t); testAccProdMutationGuard(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckLocalNetworkDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccLocalNetworkConfig(name, "10.10.0.0/24", "10.10.0.1"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("name"), knownvalue.StringExact(name)),
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("cidr"), knownvalue.StringExact("10.10.0.0/24")),
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("gateway"), knownvalue.StringExact("10.10.0.1")),
				},
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{ // Rename in place (cidr/gateway are RequiresReplace; name is mutable).
				Config: testAccLocalNetworkConfig(name+"r", "10.10.0.0/24", "10.10.0.1"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("name"), knownvalue.StringExact(name+"r")),
				},
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func testAccLocalNetworkConfig(name, cidr, gateway string) string {
	return fmt.Sprintf(`
resource "prodata_local_network" "test" {
  name    = %[1]q
  cidr    = %[2]q
  gateway = %[3]q
}
`, name, cidr, gateway)
}

func testAccCheckLocalNetworkDestroy(s *terraform.State) error {
	c, err := accClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "prodata_local_network" {
			continue
		}
		id, err := strconv.ParseInt(rs.Primary.Attributes["id"], 10, 64)
		if err != nil {
			return fmt.Errorf("parse local network id %q: %w", rs.Primary.Attributes["id"], err)
		}
		opts := &client.RequestOpts{
			Region:     rs.Primary.Attributes["region"],
			ProjectTag: rs.Primary.Attributes["project_tag"],
		}
		_, err = c.GetLocalNetwork(ctx, id, opts)
		if err == nil {
			return fmt.Errorf("local network %d still exists after destroy", id)
		}
		if !client.IsNotFound(err) {
			return fmt.Errorf("unexpected error checking destroyed local network %d: %w", id, err)
		}
	}
	return nil
}

// sweepLocalNetworks deletes acceptance local networks left behind by interrupted runs.
func sweepLocalNetworks(_ string) error {
	c, err := accClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	nets, err := c.GetLocalNetworks(ctx, nil)
	if err != nil {
		return fmt.Errorf("list local networks: %w", err)
	}
	for _, n := range nets {
		if !strings.HasPrefix(n.Name, accResourcePrefix) {
			continue
		}
		if derr := c.DeleteLocalNetwork(ctx, n.ID, nil); derr != nil && !client.IsNotFound(derr) {
			log.Printf("[WARN] sweep: failed to delete local network %d (%q): %v", n.ID, n.Name, derr)
		}
	}
	return nil
}
