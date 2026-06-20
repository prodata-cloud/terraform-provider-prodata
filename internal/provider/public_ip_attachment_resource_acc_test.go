package provider

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// TestAccPublicIPAttachment_basic attaches a public IP to a VM, asserts a clean
// post-apply plan (Read reconciles the specific attached IP), and round-trips import via
// the "vm_id:public_ip_id" form. Requires a region with an allocatable public IP pool.
func TestAccPublicIPAttachment_basic(t *testing.T) {
	name := accName()
	resourceName := "prodata_public_ip_attachment.test"
	imageID := os.Getenv("PRODATA_VM_TEST_IMAGE_ID")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheckVMImage(t); testAccProdMutationGuard(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckPublicIPAttachmentDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccPublicIPAttachmentConfig(name, imageID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("public_ip"), knownvalue.NotNull()),
				},
				// Target the attachment specifically so an unrelated drift on the VM
				// resource does not mask this check: the attachment must read back as a
				// no-op after refresh.
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionNoop),
					},
				},
			},
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
				// This resource has no synthetic "id"; correlate on public_ip_id.
				ImportStateVerifyIdentifierAttribute: "public_ip_id",
				ImportStateIdFunc:                    publicIPAttachmentImportID(resourceName),
				// public_ip is computed-from-VM and not part of the import key.
				ImportStateVerifyIgnore: []string{"public_ip"},
			},
		},
	})
}

func testAccPublicIPAttachmentConfig(name, imageID string) string {
	return fmt.Sprintf(`
resource "prodata_local_network" "test" {
  name    = %[1]q
  cidr    = "10.21.0.0/24"
  gateway = "10.21.0.1"
}

resource "prodata_vm" "test" {
  name             = %[1]q
  image_id         = %[2]s
  cpu_cores        = 1
  ram              = 2
  disk_size        = 20
  disk_type        = "SSD"
  local_network_id = prodata_local_network.test.id
  password         = "AccTestPubIPAttach123"

  timeouts = {
    create = "20m"
  }
}

resource "prodata_public_ip" "test" {
  name = %[1]q
}

resource "prodata_public_ip_attachment" "test" {
  vm_id        = prodata_vm.test.id
  public_ip_id = prodata_public_ip.test.id
}
`, name, imageID)
}

func publicIPAttachmentImportID(resourceName string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("resource %s not found in state", resourceName)
		}
		a := rs.Primary.Attributes
		return fmt.Sprintf("%s:%s", a["vm_id"], a["public_ip_id"]), nil
	}
}

// testAccCheckPublicIPAttachmentDestroy confirms the attachment is gone: the VM is
// either absent or no longer carries the expected public IP.
func testAccCheckPublicIPAttachmentDestroy(s *terraform.State) error {
	c, err := accClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "prodata_public_ip_attachment" {
			continue
		}
		vmID, err := strconv.ParseInt(rs.Primary.Attributes["vm_id"], 10, 64)
		if err != nil {
			return fmt.Errorf("parse vm_id %q: %w", rs.Primary.Attributes["vm_id"], err)
		}
		publicIPID, err := strconv.ParseInt(rs.Primary.Attributes["public_ip_id"], 10, 64)
		if err != nil {
			return fmt.Errorf("parse public_ip_id %q: %w", rs.Primary.Attributes["public_ip_id"], err)
		}
		opts := &client.RequestOpts{
			Region:     rs.Primary.Attributes["region"],
			ProjectTag: rs.Primary.Attributes["project_tag"],
		}
		vm, err := c.GetVm(ctx, vmID, opts)
		if err != nil {
			if client.IsNotFound(err) {
				continue // VM gone → attachment gone.
			}
			return fmt.Errorf("unexpected error checking destroyed public ip attachment (vm %d): %w", vmID, err)
		}
		if vm.PublicIPID == publicIPID {
			return fmt.Errorf("public ip attachment (vm %d, ip %d) still exists after destroy", vmID, publicIPID)
		}
	}
	return nil
}
