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

// TestAccLocalNetworkAttachment_basic walks one attachment through its life: attach a second
// local network to a running VM (the VM stays in its own network), an import round-trip, an
// in-place change of the restart option (the interface must not be replaced), and the two
// changes that must replace it - an explicit address, then another network.
//
// The VM is deliberately not restarted here (reboot_after_attach is only toggled on a live
// attachment); the restart path has its own test below.
func TestAccLocalNetworkAttachment_basic(t *testing.T) {
	name := accName()
	resourceName := "prodata_local_network_attachment.test"
	imageID := os.Getenv("PRODATA_VM_TEST_IMAGE_ID")

	var firstInterface string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheckVMImage(t); testAccProdMutationGuard(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckLocalNetworkAttachmentDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccLocalNetworkAttachmentConfig(name, imageID, "b", ""),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("interface_id"), knownvalue.NotNull()),
					// No address was asked for: the panel gave the next free one.
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("ip"), knownvalue.NotNull()),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith(resourceName, "interface_id", func(v string) error {
						firstInterface = v
						return nil
					}),
					testAccCheckLocalNetworkAttachmentListed(resourceName, "prodata_local_network.b"),
					// The second interface is not a move: the VM's own network is unchanged.
					testAccCheckVMStaysInNetwork("prodata_vm.test", "prodata_local_network.a"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				ResourceName:                         resourceName,
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "interface_id",
				ImportStateIdFunc:                    localNetworkAttachmentImportID(resourceName),
			},
			{
				// reboot_after_attach and timeouts are options of this resource, not of the
				// interface: changing one is an update in place, never a new interface.
				Config: testAccLocalNetworkAttachmentConfig(name, imageID, "b", "  reboot_after_attach = true\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.TestCheckResourceAttrWith(resourceName, "interface_id", func(v string) error {
					if v != firstInterface {
						return fmt.Errorf("interface_id changed from %s to %s: the interface was replaced", firstInterface, v)
					}
					return nil
				}),
			},
			{
				// A requested address is part of the interface's identity: changing it replaces it,
				// and after the replace the plan is empty again (the address matches the config).
				Config: testAccLocalNetworkAttachmentConfig(name, imageID, "b", "  ip = \"192.168.211.50\"\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionDestroyBeforeCreate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("ip"), knownvalue.StringExact("192.168.211.50")),
				},
			},
			{
				Config: testAccLocalNetworkAttachmentConfig(name, imageID, "c", "  ip = \"192.168.212.50\"\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionDestroyBeforeCreate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: testAccCheckLocalNetworkAttachmentListed(resourceName, "prodata_local_network.c"),
			},
		},
	})
}

// TestAccLocalNetworkAttachment_rebootAfterAttach asks for the restart the platform needs for
// the interface to appear in a running VM, and checks the VM is back and the interface is there.
func TestAccLocalNetworkAttachment_rebootAfterAttach(t *testing.T) {
	name := accName()
	resourceName := "prodata_local_network_attachment.test"
	imageID := os.Getenv("PRODATA_VM_TEST_IMAGE_ID")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheckVMImage(t); testAccProdMutationGuard(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckLocalNetworkAttachmentDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccLocalNetworkAttachmentConfig(name, imageID, "b", "  reboot_after_attach = true\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckVMStatus("prodata_vm.test", "RUNNING"),
					testAccCheckLocalNetworkAttachmentListed(resourceName, "prodata_local_network.b"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// testAccLocalNetworkAttachmentConfig is a VM in network "a" with the attachment in network
// "network" (a, b or c); extra goes inside the attachment block, one attribute per line. The
// address ranges are throw-away ones that never meet a real network.
func testAccLocalNetworkAttachmentConfig(name, imageID, network, extra string) string {
	return fmt.Sprintf(`
resource "prodata_local_network" "a" {
  name    = "%[1]s-a"
  cidr    = "192.168.210.0/24"
  gateway = "192.168.210.1"
}

resource "prodata_local_network" "b" {
  name    = "%[1]s-b"
  cidr    = "192.168.211.0/24"
  gateway = "192.168.211.1"
}

resource "prodata_local_network" "c" {
  name    = "%[1]s-c"
  cidr    = "192.168.212.0/24"
  gateway = "192.168.212.1"
}

resource "prodata_vm" "test" {
  name             = %[1]q
  image_id         = %[2]s
  cpu_cores        = 1
  ram              = 1
  disk_size        = 20
  disk_type        = "SSD"
  local_network_id = prodata_local_network.a.id
  password         = "AccTestLocalNic123"

  timeouts = {
    create = "20m"
  }
}

resource "prodata_local_network_attachment" "test" {
  vm_id      = prodata_vm.test.id
  network_id = prodata_local_network.%[3]s.id
%[4]s}
`, name, imageID, network, extra)
}

func localNetworkAttachmentImportID(resourceName string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("resource %s not found in state", resourceName)
		}
		a := rs.Primary.Attributes
		return fmt.Sprintf("%s:%s", a["vm_id"], a["interface_id"]), nil
	}
}

// accIDs reads the numeric ids the checks below need from a resource's state.
func accIDs(s *terraform.State, address string, attrs ...string) (map[string]int64, map[string]string, error) {
	rs, ok := s.RootModule().Resources[address]
	if !ok {
		return nil, nil, fmt.Errorf("resource %s not found in state", address)
	}
	ids := make(map[string]int64, len(attrs))
	for _, attr := range attrs {
		v, err := strconv.ParseInt(rs.Primary.Attributes[attr], 10, 64)
		if err != nil {
			return nil, nil, fmt.Errorf("%s.%s = %q is not a number: %w", address, attr, rs.Primary.Attributes[attr], err)
		}
		ids[attr] = v
	}
	return ids, rs.Primary.Attributes, nil
}

// testAccCheckLocalNetworkAttachmentListed confirms the panel lists the attachment's interface,
// in the network the config named, as a second (non-primary) interface of the VM.
func testAccCheckLocalNetworkAttachmentListed(resourceName, networkAddress string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		att, attrs, err := accIDs(s, resourceName, "vm_id", "interface_id", "network_id")
		if err != nil {
			return err
		}
		net, _, err := accIDs(s, networkAddress, "id")
		if err != nil {
			return err
		}
		if att["network_id"] != net["id"] {
			return fmt.Errorf("attachment network_id %d is not %s (%d)", att["network_id"], networkAddress, net["id"])
		}
		c, err := accClient()
		if err != nil {
			return err
		}
		opts := &client.RequestOpts{Region: attrs["region"], ProjectTag: attrs["project_tag"]}
		nics, err := c.GetVmLocalNetworks(context.Background(), att["vm_id"], opts)
		if err != nil {
			return fmt.Errorf("list local networks of VM %d: %w", att["vm_id"], err)
		}
		for _, n := range nics {
			if n.ID != att["interface_id"] {
				continue
			}
			if n.Primary {
				return fmt.Errorf("interface %d of VM %d is listed as primary", n.ID, att["vm_id"])
			}
			if n.NetworkID == nil || *n.NetworkID != net["id"] {
				return fmt.Errorf("interface %d of VM %d is not in network %d", n.ID, att["vm_id"], net["id"])
			}
			return nil
		}
		return fmt.Errorf("interface %d is not among the %d local interfaces of VM %d", att["interface_id"], len(nics), att["vm_id"])
	}
}

// testAccCheckVMStaysInNetwork confirms the VM's own network is the one in the config: a second
// interface does not move the VM.
func testAccCheckVMStaysInNetwork(vmAddress, networkAddress string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		vm, attrs, err := accIDs(s, vmAddress, "id")
		if err != nil {
			return err
		}
		net, _, err := accIDs(s, networkAddress, "id")
		if err != nil {
			return err
		}
		c, err := accClient()
		if err != nil {
			return err
		}
		opts := &client.RequestOpts{Region: attrs["region"], ProjectTag: attrs["project_tag"]}
		got, err := c.GetVm(context.Background(), vm["id"], opts)
		if err != nil {
			return fmt.Errorf("read VM %d: %w", vm["id"], err)
		}
		if got.LocalNetworkID != net["id"] {
			return fmt.Errorf("VM %d is in network %d, want %d", vm["id"], got.LocalNetworkID, net["id"])
		}
		return nil
	}
}

func testAccCheckVMStatus(vmAddress, want string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		vm, attrs, err := accIDs(s, vmAddress, "id")
		if err != nil {
			return err
		}
		c, err := accClient()
		if err != nil {
			return err
		}
		opts := &client.RequestOpts{Region: attrs["region"], ProjectTag: attrs["project_tag"]}
		got, err := c.GetVmStatus(context.Background(), vm["id"], opts)
		if err != nil {
			return fmt.Errorf("read status of VM %d: %w", vm["id"], err)
		}
		if got.Status != want {
			return fmt.Errorf("VM %d is %s, want %s", vm["id"], got.Status, want)
		}
		return nil
	}
}

// testAccCheckLocalNetworkAttachmentDestroy confirms the interface is gone: either the VM is no
// longer there, or its list of local interfaces no longer holds the attachment's id.
func testAccCheckLocalNetworkAttachmentDestroy(s *terraform.State) error {
	c, err := accClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "prodata_local_network_attachment" {
			continue
		}
		vmID, err := strconv.ParseInt(rs.Primary.Attributes["vm_id"], 10, 64)
		if err != nil {
			return fmt.Errorf("parse vm_id %q: %w", rs.Primary.Attributes["vm_id"], err)
		}
		interfaceID, err := strconv.ParseInt(rs.Primary.Attributes["interface_id"], 10, 64)
		if err != nil {
			return fmt.Errorf("parse interface_id %q: %w", rs.Primary.Attributes["interface_id"], err)
		}
		opts := &client.RequestOpts{
			Region:     rs.Primary.Attributes["region"],
			ProjectTag: rs.Primary.Attributes["project_tag"],
		}
		nics, err := c.GetVmLocalNetworks(ctx, vmID, opts)
		if err != nil {
			if client.IsNotFound(err) {
				continue // VM gone -> interface gone.
			}
			return fmt.Errorf("unexpected error checking destroyed attachment (vm %d): %w", vmID, err)
		}
		for _, n := range nics {
			if n.ID == interfaceID {
				return fmt.Errorf("interface %d of VM %d still exists after destroy", interfaceID, vmID)
			}
		}
	}
	return nil
}
