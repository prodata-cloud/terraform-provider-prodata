package provider

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// TestAccVM_movePrimaryNetwork moves a running VM to another local network by changing
// local_network_id, and back with an address of its own. Both are updates in place: the VM keeps
// its id, is in the network the panel reports, has an address of it, and is RUNNING again. The last
// step is the other half of the contract: a new address alone, in the network the VM is in, still
// replaces the VM.
//
// It stops and starts a real VM twice and creates a second one in the end, so it runs only with the
// operator's word, like the other acceptance tests that mutate (see testAccProdMutationGuard).
func TestAccVM_movePrimaryNetwork(t *testing.T) {
	name := accName()
	resourceName := "prodata_vm.mover"
	imageID := os.Getenv("PRODATA_VM_TEST_IMAGE_ID")

	var firstID string
	sameVM := func(s string) error {
		if s != firstID {
			return fmt.Errorf("the VM id changed from %s to %s: the VM was replaced, not moved", firstID, s)
		}
		return nil
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheckVMImage(t); testAccProdMutationGuard(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: resource.ComposeAggregateTestCheckFunc(
			testAccCheckVmDestroy,
			testAccCheckLocalNetworkDestroy,
		),
		Steps: []resource.TestStep{
			{
				Config: testAccVMMoveConfig(name, imageID, "a", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith(resourceName, "id", func(v string) error {
						firstID = v
						return nil
					}),
					testAccCheckVMAddressIn(resourceName, "192.168.220."),
					testAccCheckVMStaysInNetwork(resourceName, "prodata_local_network.a"),
					testAccCheckVMRunning(resourceName),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// No address was asked for: the panel gives the next free one of network b, and
				// the plan can only say so after apply.
				Config: testAccVMMoveConfig(name, imageID, "b", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionUpdate),
						plancheck.ExpectUnknownValue(resourceName, tfjsonpath.New("private_ip")),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith(resourceName, "id", sameVM),
					testAccCheckVMAddressIn(resourceName, "192.168.221."),
					testAccCheckVMStaysInNetwork(resourceName, "prodata_local_network.b"),
					testAccCheckVMRunning(resourceName),
				),
			},
			{
				// Back to network a, with an address of the caller's choice, set together with
				// the network.
				Config: testAccVMMoveConfig(name, imageID, "a", "192.168.220.50"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith(resourceName, "id", sameVM),
					resource.TestCheckResourceAttr(resourceName, "private_ip", "192.168.220.50"),
					testAccCheckVMStaysInNetwork(resourceName, "prodata_local_network.a"),
					testAccCheckVMRunning(resourceName),
				),
			},
			{
				// A new address with the network unchanged is not a move: the VM is replaced.
				Config: testAccVMMoveConfig(name, imageID, "a", "192.168.220.51"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionDestroyBeforeCreate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.TestCheckResourceAttr(resourceName, "private_ip", "192.168.220.51"),
			},
		},
	})
}

// testAccVMMoveConfig is a VM in network "network" (a or b), with privateIP when it is not empty.
// The address ranges are throw-away ones that never meet a real network.
func testAccVMMoveConfig(name, imageID, network, privateIP string) string {
	address := ""
	if privateIP != "" {
		address = fmt.Sprintf("  private_ip       = %q\n", privateIP)
	}
	return fmt.Sprintf(`
resource "prodata_local_network" "a" {
  name    = "%[1]s-a"
  cidr    = "192.168.220.0/24"
  gateway = "192.168.220.1"
}

resource "prodata_local_network" "b" {
  name    = "%[1]s-b"
  cidr    = "192.168.221.0/24"
  gateway = "192.168.221.1"
}

resource "prodata_vm" "mover" {
  name             = %[1]q
  image_id         = %[2]s
  cpu_cores        = 1
  ram              = 1
  disk_size        = 20
  disk_type        = "SSD"
  local_network_id = prodata_local_network.%[3]s.id
%[4]s  password         = "AccTestMoveNic123"

  timeouts = {
    create = "20m"
    update = "30m"
  }
}
`, name, imageID, network, address)
}

// testAccCheckVMAddressIn confirms the VM's address in the state starts with prefix, so that it is
// an address of the network the config named.
func testAccCheckVMAddressIn(vmAddress, prefix string) resource.TestCheckFunc {
	return resource.TestCheckResourceAttrWith(vmAddress, "private_ip", func(v string) error {
		if !strings.HasPrefix(v, prefix) {
			return fmt.Errorf("private_ip %q is not in %s*", v, prefix)
		}
		return nil
	})
}
