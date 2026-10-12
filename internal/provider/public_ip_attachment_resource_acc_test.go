package provider

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

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

// TestAccPublicIPAttachment_rebootAfterAttach attaches a public IP with reboot_after_attach = true
// to a running VM. A background sampler watches the VM's status while the attachment is created:
// the VM has to be seen stopping or stopped, and by the time apply returns it is RUNNING again and
// carries the IP. The VM is created first, in a step of its own, so that the restart is the attach
// step's doing and nothing else's. The plan after apply is clean; flipping the argument afterwards
// is an in-place update, and an imported attachment does not carry it. Whether the address answers
// is not checked: that depends on the guest and the security group, not on the provider.
// Requires a region with an allocatable public IP pool and compute capacity.
func TestAccPublicIPAttachment_rebootAfterAttach(t *testing.T) {
	name := accName()
	resourceName := "prodata_public_ip_attachment.test"
	imageID := os.Getenv("PRODATA_VM_TEST_IMAGE_ID")

	var (
		vmID    int64
		vmOpts  *client.RequestOpts
		watcher *vmStatusSampler
	)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheckVMImage(t); testAccProdMutationGuard(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckPublicIPAttachmentDestroy,
		Steps: []resource.TestStep{
			{
				// A running VM and a public IP that is not attached yet.
				Config: testAccPublicIPAttachmentBaseConfig(name, imageID),
				Check: func(s *terraform.State) error {
					rs, ok := s.RootModule().Resources["prodata_vm.test"]
					if !ok {
						return fmt.Errorf("resource prodata_vm.test not found in state")
					}
					id, err := strconv.ParseInt(rs.Primary.Attributes["id"], 10, 64)
					if err != nil {
						return fmt.Errorf("parse VM id %q: %w", rs.Primary.Attributes["id"], err)
					}
					vmID = id
					vmOpts = &client.RequestOpts{Region: rs.Primary.Attributes["region"], ProjectTag: rs.Primary.Attributes["project_tag"]}
					return nil
				},
			},
			{
				PreConfig: func() { watcher = startVMStatusSampler(t, vmID, vmOpts) },
				Config:    testAccPublicIPAttachmentConfigWith(name, imageID, "  reboot_after_attach = true\n"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("reboot_after_attach"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("public_ip"), knownvalue.NotNull()),
				},
				Check: resource.ComposeTestCheckFunc(
					testAccCheckPublicIPAttachmentVM(resourceName, "RUNNING"),
					func(*terraform.State) error {
						seen := watcher.stop()
						// Sampling is periodic, so a status that lasts under a second can be missed. Either of
						// two is enough: the panel holds STOPPING until the hypervisor has finished the stop,
						// and the VM stays STOPPED until the provider's next poll finds it and starts it.
						if !seen["STOPPING"] && !seen["STOPPED"] {
							return fmt.Errorf("the VM was seen only as %v while the attachment was created: it was not stopped and started again", slices.Sorted(maps.Keys(seen)))
						}
						return nil
					},
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionNoop),
					},
				},
			},
			{
				// The argument is read only at create, so changing it is recorded and does nothing else.
				Config: testAccPublicIPAttachmentConfigWith(name, imageID, "  reboot_after_attach = false\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionUpdate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("reboot_after_attach"), knownvalue.Bool(false)),
				},
				Check: testAccCheckPublicIPAttachmentVM(resourceName, "RUNNING"),
			},
			{
				ResourceName:                         resourceName,
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "public_ip_id",
				ImportStateIdFunc:                    publicIPAttachmentImportID(resourceName),
				// None of these is read from the platform: public_ip is filled in by the first
				// refresh, the other two are configuration only.
				ImportStateVerifyIgnore: []string{"public_ip", "reboot_after_attach", "timeouts"},
			},
		},
	})
}

func testAccPublicIPAttachmentConfig(name, imageID string) string {
	return testAccPublicIPAttachmentConfigWith(name, imageID, "")
}

// testAccPublicIPAttachmentBaseConfig is a running VM on a local network and a public IP that
// is not attached to anything. The panel keeps local networks and public IPs in one namespace
// (per account and region), so the public IP has a name of its own: under one name, whichever of
// the two is created second is refused with 614.
func testAccPublicIPAttachmentBaseConfig(name, imageID string) string {
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
  name = %[3]q
}
`, name, imageID, name+"-ip")
}

// testAccPublicIPAttachmentConfigWith is the base configuration and the attachment, with extra
// lines inside the attachment block, each ending in a newline.
func testAccPublicIPAttachmentConfigWith(name, imageID, attachmentExtra string) string {
	return testAccPublicIPAttachmentBaseConfig(name, imageID) + fmt.Sprintf(`
resource "prodata_public_ip_attachment" "test" {
  vm_id        = prodata_vm.test.id
  public_ip_id = prodata_public_ip.test.id
%s}
`, attachmentExtra)
}

// vmStatusSampler records every status a VM reports until it is stopped. The v2 API says nothing
// about a VM's uptime or about the power operations done to it, so a restart can only be seen as
// the statuses the VM passes through.
type vmStatusSampler struct {
	mu   sync.Mutex
	seen map[string]bool

	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

const vmStatusSampleEvery = time.Second

// startVMStatusSampler starts sampling the VM's status in the background. The sampler is stopped
// when the test ends if it has not been stopped before.
func startVMStatusSampler(t *testing.T, vmID int64, opts *client.RequestOpts) *vmStatusSampler {
	t.Helper()
	c, err := accClient()
	if err != nil {
		t.Fatalf("building the client for the status sampler: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &vmStatusSampler{seen: map[string]bool{}, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		for {
			// A read that is under way when the sampler is stopped is cut short with the context.
			if vm, err := c.GetVmStatus(ctx, vmID, opts); err == nil {
				s.mu.Lock()
				s.seen[vm.Status] = true
				s.mu.Unlock()
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(vmStatusSampleEvery):
			}
		}
	}()
	t.Cleanup(func() { s.stop() })
	return s
}

// stop ends the sampling and returns the statuses seen.
func (s *vmStatusSampler) stop() map[string]bool {
	s.once.Do(s.cancel)
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.seen)
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

// testAccCheckPublicIPAttachmentVM confirms that the VM under the attachment is in the given
// status and carries the attached public IP.
func testAccCheckPublicIPAttachmentVM(resourceName, wantStatus string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource %s not found in state", resourceName)
		}
		a := rs.Primary.Attributes
		vmID, err := strconv.ParseInt(a["vm_id"], 10, 64)
		if err != nil {
			return fmt.Errorf("parse vm_id %q: %w", a["vm_id"], err)
		}
		publicIPID, err := strconv.ParseInt(a["public_ip_id"], 10, 64)
		if err != nil {
			return fmt.Errorf("parse public_ip_id %q: %w", a["public_ip_id"], err)
		}
		c, err := accClient()
		if err != nil {
			return err
		}
		vm, err := c.GetVm(context.Background(), vmID, &client.RequestOpts{Region: a["region"], ProjectTag: a["project_tag"]})
		if err != nil {
			return fmt.Errorf("reading VM %d: %w", vmID, err)
		}
		if vm.Status != wantStatus {
			return fmt.Errorf("VM %d is %s, want %s", vmID, vm.Status, wantStatus)
		}
		if vm.PublicIPID != publicIPID {
			return fmt.Errorf("VM %d carries public IP %d, want %d", vmID, vm.PublicIPID, publicIPID)
		}
		return nil
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
