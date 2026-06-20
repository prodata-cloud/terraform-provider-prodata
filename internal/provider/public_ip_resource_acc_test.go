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
	resource.AddTestSweepers("prodata_public_ip", &resource.Sweeper{
		Name: "prodata_public_ip",
		F:    sweepPublicIPs,
	})
}

// TestAccPublicIP_basic exercises create+read, an in-place rename, plan stability,
// and an import round-trip of prodata_public_ip.
func TestAccPublicIP_basic(t *testing.T) {
	name := accName()
	resourceName := "prodata_public_ip.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t); testAccProdMutationGuard(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckPublicIPDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccPublicIPConfig(name),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("name"), knownvalue.StringExact(name)),
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("ip"), knownvalue.NotNull()),
				},
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{ // Rename in place.
				Config: testAccPublicIPConfig(name + "r"),
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

func testAccPublicIPConfig(name string) string {
	return fmt.Sprintf(`
resource "prodata_public_ip" "test" {
  name = %[1]q
}
`, name)
}

func testAccCheckPublicIPDestroy(s *terraform.State) error {
	c, err := accClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "prodata_public_ip" {
			continue
		}
		id, err := strconv.ParseInt(rs.Primary.Attributes["id"], 10, 64)
		if err != nil {
			return fmt.Errorf("parse public ip id %q: %w", rs.Primary.Attributes["id"], err)
		}
		opts := &client.RequestOpts{
			Region:     rs.Primary.Attributes["region"],
			ProjectTag: rs.Primary.Attributes["project_tag"],
		}
		_, err = c.GetPublicIP(ctx, id, opts)
		if err == nil {
			return fmt.Errorf("public ip %d still exists after destroy", id)
		}
		if !client.IsNotFound(err) {
			return fmt.Errorf("unexpected error checking destroyed public ip %d: %w", id, err)
		}
	}
	return nil
}

// sweepPublicIPs deletes acceptance public IPs left behind by interrupted runs.
func sweepPublicIPs(_ string) error {
	c, err := accClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	ips, err := c.GetPublicIPs(ctx, nil)
	if err != nil {
		return fmt.Errorf("list public ips: %w", err)
	}
	for _, ip := range ips {
		if !strings.HasPrefix(ip.Name, accResourcePrefix) {
			continue
		}
		if derr := c.DeletePublicIP(ctx, ip.ID, nil); derr != nil && !client.IsNotFound(derr) {
			log.Printf("[WARN] sweep: failed to delete public ip %d (%q): %v", ip.ID, ip.Name, derr)
		}
	}
	return nil
}
