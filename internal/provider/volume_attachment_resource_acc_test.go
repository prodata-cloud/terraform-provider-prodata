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

// testAccPreCheckVMImage gates tests that must boot a real VM. The network is created
// inline by the test config, so only a bootable image id is required from the env.
func testAccPreCheckVMImage(t *testing.T) {
	t.Helper()
	testAccPreCheck(t)
	if os.Getenv("PRODATA_VM_TEST_IMAGE_ID") == "" {
		t.Skip("PRODATA_VM_TEST_IMAGE_ID must be set for VM-backed acceptance tests")
	}
}

// TestAccVolumeAttachment_basic is the regression witness for the volume_attachment
// Read fix (B1): after attaching a volume to a VM, a refresh+plan must be EMPTY. Before
// the fix, Read looked the attachment up by the wrong id space (GetVolume on a VmDisk
// id), 404'd, and dropped the resource from state — which a post-apply refresh would
// surface as a spurious re-create. The import step covers the "vm_id:volume_id" path.
func TestAccVolumeAttachment_basic(t *testing.T) {
	name := accName()
	resourceName := "prodata_volume_attachment.test"
	imageID := os.Getenv("PRODATA_VM_TEST_IMAGE_ID")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheckVMImage(t); testAccProdMutationGuard(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckVolumeAttachmentDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccVolumeAttachmentConfig(name, imageID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("attached_volume_id"), knownvalue.NotNull()),
				},
				// The B1 assertion: after refresh the attachment itself must be a no-op.
				// (Targeted at the attachment resource so an unrelated drift on the VM
				// resource does not mask the regression this test guards.) Before the
				// fix, Read dropped the attachment from state, so this would be a Create.
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
				// This resource has no synthetic "id"; correlate on the computed VmDisk id.
				ImportStateVerifyIdentifierAttribute: "attached_volume_id",
				ImportStateIdFunc:                    volumeAttachmentImportID(resourceName),
			},
		},
	})
}

func testAccVolumeAttachmentConfig(name, imageID string) string {
	return fmt.Sprintf(`
resource "prodata_local_network" "test" {
  name    = %[1]q
  cidr    = "10.20.0.0/24"
  gateway = "10.20.0.1"
}

resource "prodata_vm" "test" {
  name             = %[1]q
  image_id         = %[2]s
  cpu_cores        = 1
  ram              = 2
  disk_size        = 20
  disk_type        = "SSD"
  local_network_id = prodata_local_network.test.id
  password         = "AccTestVolAttach123"

  timeouts = {
    create = "20m"
  }
}

resource "prodata_volume" "test" {
  name = %[1]q
  type = "HDD"
  size = 10
}

resource "prodata_volume_attachment" "test" {
  vm_id     = prodata_vm.test.id
  volume_id = prodata_volume.test.id
}
`, name, imageID)
}

func volumeAttachmentImportID(resourceName string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("resource %s not found in state", resourceName)
		}
		a := rs.Primary.Attributes
		return fmt.Sprintf("%s:%s", a["vm_id"], a["volume_id"]), nil
	}
}

// testAccCheckVolumeAttachmentDestroy confirms the attachment is gone: either the VM is
// no longer present, or its disk list no longer contains the attached VmDisk id.
func testAccCheckVolumeAttachmentDestroy(s *terraform.State) error {
	c, err := accClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "prodata_volume_attachment" {
			continue
		}
		vmID, err := strconv.ParseInt(rs.Primary.Attributes["vm_id"], 10, 64)
		if err != nil {
			return fmt.Errorf("parse vm_id %q: %w", rs.Primary.Attributes["vm_id"], err)
		}
		attachedID, err := strconv.ParseInt(rs.Primary.Attributes["attached_volume_id"], 10, 64)
		if err != nil {
			return fmt.Errorf("parse attached_volume_id %q: %w", rs.Primary.Attributes["attached_volume_id"], err)
		}
		opts := &client.RequestOpts{
			Region:     rs.Primary.Attributes["region"],
			ProjectTag: rs.Primary.Attributes["project_tag"],
		}
		disks, err := c.GetVmVolumes(ctx, vmID, opts)
		if err != nil {
			if client.IsNotFound(err) {
				continue // VM gone → attachment gone.
			}
			return fmt.Errorf("unexpected error checking destroyed volume attachment (vm %d): %w", vmID, err)
		}
		for _, d := range disks {
			if d.ID == attachedID {
				return fmt.Errorf("volume attachment (vm %d, disk %d) still exists after destroy", vmID, attachedID)
			}
		}
	}
	return nil
}
