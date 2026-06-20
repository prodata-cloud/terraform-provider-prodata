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
	resource.AddTestSweepers("prodata_volume", &resource.Sweeper{
		Name: "prodata_volume",
		F:    sweepVolumes,
	})
}

// TestAccVolume_outOfBandDeleteDetected is the regression witness for the volume
// soft-delete drift fix: GET /volumes/{id} keeps returning a deleted volume
// (success:true) while the list endpoint drops it, so Read must confirm liveness via
// the list. After deleting the volume out-of-band, the refresh plan must be non-empty
// (Terraform plans to recreate it). Without the fix, by-id Read keeps stale state and
// the plan would be empty.
func TestAccVolume_outOfBandDeleteDetected(t *testing.T) {
	name := accName()
	resourceName := "prodata_volume.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t); testAccProdMutationGuard(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckVolumeDestroy,
		Steps: []resource.TestStep{
			{
				Config:             testAccVolumeConfig(name, "HDD", 10),
				Check:              testAccVolumeDisappears(resourceName),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// testAccVolumeDisappears deletes the volume out-of-band (directly via the API),
// simulating a deletion made outside Terraform.
func testAccVolumeDisappears(resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource %s not found in state", resourceName)
		}
		c, err := accClient()
		if err != nil {
			return err
		}
		id, err := strconv.ParseInt(rs.Primary.Attributes["id"], 10, 64)
		if err != nil {
			return fmt.Errorf("parse volume id %q: %w", rs.Primary.Attributes["id"], err)
		}
		opts := &client.RequestOpts{
			Region:     rs.Primary.Attributes["region"],
			ProjectTag: rs.Primary.Attributes["project_tag"],
		}
		return c.DeleteVolume(context.Background(), id, opts)
	}
}

// TestAccVolume_basic exercises create+read, an in-place rename, plan stability, and
// an import round-trip of prodata_volume.
func TestAccVolume_basic(t *testing.T) {
	name := accName()
	resourceName := "prodata_volume.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t); testAccProdMutationGuard(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckVolumeDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccVolumeConfig(name, "HDD", 10),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("name"), knownvalue.StringExact(name)),
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("type"), knownvalue.StringExact("HDD")),
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("size"), knownvalue.Int64Exact(10)),
				},
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{ // Rename in place (size/type are RequiresReplace; name is mutable).
				Config: testAccVolumeConfig(name+"r", "HDD", 10),
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

func testAccVolumeConfig(name, volType string, size int64) string {
	return fmt.Sprintf(`
resource "prodata_volume" "test" {
  name = %[1]q
  type = %[2]q
  size = %[3]d
}
`, name, volType, size)
}

func testAccCheckVolumeDestroy(s *terraform.State) error {
	c, err := accClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "prodata_volume" {
			continue
		}
		id, err := strconv.ParseInt(rs.Primary.Attributes["id"], 10, 64)
		if err != nil {
			return fmt.Errorf("parse volume id %q: %w", rs.Primary.Attributes["id"], err)
		}
		opts := &client.RequestOpts{
			Region:     rs.Primary.Attributes["region"],
			ProjectTag: rs.Primary.Attributes["project_tag"],
		}
		// The by-id GET /volumes/{id} endpoint keeps returning soft-deleted volumes
		// (success:true) after deletion, whereas the list endpoint correctly drops
		// them — so the list is the authoritative signal that a destroy succeeded.
		volumes, err := c.GetVolumes(ctx, opts)
		if err != nil {
			return fmt.Errorf("listing volumes to verify destroy: %w", err)
		}
		for _, v := range volumes {
			if v.ID == id {
				return fmt.Errorf("volume %d still exists after destroy", id)
			}
		}
	}
	return nil
}

// sweepVolumes deletes acceptance volumes left behind by interrupted runs.
func sweepVolumes(_ string) error {
	c, err := accClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	volumes, err := c.GetVolumes(ctx, nil)
	if err != nil {
		return fmt.Errorf("list volumes: %w", err)
	}
	for _, v := range volumes {
		if !strings.HasPrefix(v.Name, accResourcePrefix) {
			continue
		}
		if derr := c.DeleteVolume(ctx, v.ID, nil); derr != nil && !client.IsNotFound(derr) {
			log.Printf("[WARN] sweep: failed to delete volume %d (%q): %v", v.ID, v.Name, derr)
		}
	}
	return nil
}
