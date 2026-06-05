package provider

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
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

func init() {
	resource.AddTestSweepers("prodata_vm", &resource.Sweeper{
		Name: "prodata_vm",
		F:    sweepVms,
	})
}

// guestMarkerPath is where the test user_data writes its nonce marker; the SSH
// witness reads it back to prove cloud-init actually ran the payload.
const guestMarkerPath = "/var/lib/prodata-acc-marker"

// testAccPreCheckVmUserData gates the user_data lifecycle tests on a real image +
// network. The image must boot cloud-init and the qemu-guest-agent so the backend's
// in-guest cloud-init wait can detect first boot.
func testAccPreCheckVmUserData(t *testing.T) {
	t.Helper()
	testAccPreCheck(t)
	for _, k := range []string{"PRODATA_VM_TEST_IMAGE_ID", "PRODATA_VM_TEST_NET_ID"} {
		if os.Getenv(k) == "" {
			t.Skipf("%s must be set for prodata_vm user_data acceptance tests", k)
		}
	}
}

// sshEnabled reports whether in-guest SSH verification is wired up. Without it the
// lifecycle tests still assert create / RUNNING / empty-plan / replacement, but cannot
// prove cloud-init succeeded — which the VM status alone never reveals.
func sshEnabled(t *testing.T) bool {
	t.Helper()
	if os.Getenv("PRODATA_VM_TEST_SSH_REACHABLE") != "1" {
		return false
	}
	for _, k := range []string{"PRODATA_VM_TEST_SSH_KEY", "PRODATA_VM_TEST_SSH_PRIVKEY", "PRODATA_VM_TEST_PUBLIC_IP_ID"} {
		if os.Getenv(k) == "" {
			t.Logf("PRODATA_VM_TEST_SSH_REACHABLE=1 but %s is unset — skipping in-guest marker verification", k)
			return false
		}
	}
	return true
}

// TestAccVm_userData_invalidRejectedAtPlan asserts the client-side validator rejects a
// malformed payload at plan time, before anything reaches the API (where the backend would
// then reject it with a confusing generic error). Dummy image/network ids are fine:
// validation fails first, so no infrastructure is created and the test needs no test-stand
// resources.
func TestAccVm_userData_invalidRejectedAtPlan(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccVmUserDataConfigInvalid(accName()),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?i)#cloud-config`),
			},
		},
	})
}

// TestAccVm_userData_missingHashRejected asserts the ModifyPlan consistency check: a
// payload with no user_data_hash is rejected at plan, because a write-only value with no
// hash trigger would make later edits invisible. No infrastructure is created.
func TestAccVm_userData_missingHashRejected(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccVmUserDataConfigNoHash(accName()),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?i)user_data_hash`),
			},
		},
	})
}

// TestAccVm_userData_lifecycle is the core test-stand witness: create a VM with a
// #cloud-config that writes a per-run nonce marker, assert it reaches RUNNING with a
// stable plan, prove the raw payload never landed in state (canary), and — when SSH is
// wired — prove the marker actually exists on the guest. Then change the payload and
// assert the VM is REPLACED (destroy-before-create) and the new marker is present.
func TestAccVm_userData_lifecycle(t *testing.T) {
	name := accName()
	resourceName := "prodata_vm.test"
	marker1 := "marker-" + strings.TrimPrefix(name, accResourcePrefix)
	marker2 := marker1 + "-v2"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheckVmUserData(t); testAccProdMutationGuard(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckVmDestroy,
		Steps: []resource.TestStep{
			{ // Create with a cloud-config that writes marker1.
				Config: testAccVmUserDataConfig(name, marker1),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("status"), knownvalue.StringExact("RUNNING")),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					// The write-only payload must never appear in state (security invariant).
					checkCanaryAbsentFromState(marker1),
					// And, when SSH is available, it must actually be on the guest.
					checkGuestMarker(t, resourceName, marker1, true),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{ // Change the payload+hash: the VM must be replaced (cloud-init only runs once).
				Config: testAccVmUserDataConfig(name, marker2),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					checkGuestMarker(t, resourceName, marker2, true),
				),
			},
		},
	})
}

// TestAccVm_userData_knownBadWitness is the meta-test that proves the SSH witness can
// actually fail: the marker write is gated behind a runcmd that exits non-zero, so the
// marker never appears. If SSH verification is enabled the marker check MUST fail; this
// guards against a witness that silently passes on broken cloud-init. Skipped when SSH
// verification is not wired (there would be nothing to prove).
func TestAccVm_userData_knownBadWitness(t *testing.T) {
	if !sshEnabled(t) {
		t.Skip("in-guest SSH verification not enabled (PRODATA_VM_TEST_SSH_REACHABLE=1 + key/privkey/public-ip) — nothing to witness")
	}
	name := accName()
	resourceName := "prodata_vm.test"
	marker := "marker-" + strings.TrimPrefix(name, accResourcePrefix)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheckVmUserData(t); testAccProdMutationGuard(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckVmDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccVmUserDataConfigKnownBad(name, marker),
				// mustSucceed=false: the marker must be ABSENT, so the witness must report failure.
				Check: checkGuestMarker(t, resourceName, marker, false),
			},
		},
	})
}

// TestAccVm_userData_importNoReplace imports a user_data VM (write-only attrs are absent
// from state after import) and asserts that re-applying with the payload set adopts it via
// an in-place update (adopts the hash, no replacement) — the import-aware WriteOnceString
// behavior. A stock RequiresReplace on user_data_hash would destroy the VM here.
func TestAccVm_userData_importNoReplace(t *testing.T) {
	name := accName()
	resourceName := "prodata_vm.test"
	marker := "marker-" + strings.TrimPrefix(name, accResourcePrefix)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheckVmUserData(t); testAccProdMutationGuard(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckVmDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccVmUserDataConfig(name, marker),
			},
			{ // Import: user_data (write-only) and user_data_hash are not read back.
				ResourceName:            resourceName,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"password", "ssh_public_key", "user_data", "user_data_hash", "timeouts"},
				ImportStateIdFunc:       vmImportID(resourceName),
			},
			{ // Re-apply same config: an in-place update (adopts the hash, no replacement).
				// After import, password + user_data_hash go null->value, so the real plan
				// is an in-place UPDATE, never a no-op; Update IS the no-replace proof.
				Config: testAccVmUserDataConfig(name, marker),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionUpdate),
					},
				},
			},
		},
	})
}

// ---- config builders ----

func vmPublicIPLine() string {
	if id := os.Getenv("PRODATA_VM_TEST_PUBLIC_IP_ID"); id != "" {
		return "  public_ip_id     = " + id + "\n"
	}
	return ""
}

func vmSSHKeyLine() string {
	if k := os.Getenv("PRODATA_VM_TEST_SSH_KEY"); k != "" {
		return "  ssh_public_key   = " + strconv.Quote(k) + "\n"
	}
	return ""
}

// testAccVmUserDataConfig writes a nonce marker via cloud-init write_files. The marker
// content embeds the canary so the raw-not-in-state assertion is meaningful.
func testAccVmUserDataConfig(name, marker string) string {
	return fmt.Sprintf(`
locals {
  ud = <<-EOT
    #cloud-config
    write_files:
      - path: %[4]s
        content: %[5]q
    runcmd:
      - [ systemctl, enable, --now, qemu-guest-agent ]
  EOT
}

resource "prodata_vm" "test" {
  name             = %[1]q
  image_id         = %[2]s
  cpu_cores        = 1
  ram              = 2
  disk_size        = 20
  disk_type        = "SSD"
  local_network_id = %[3]s
  password         = "AccTestUserData123"
%[6]s%[7]s
  user_data      = local.ud
  user_data_hash = sha256(local.ud)

  timeouts {
    create = "15m"
  }
}
`, name, os.Getenv("PRODATA_VM_TEST_IMAGE_ID"), os.Getenv("PRODATA_VM_TEST_NET_ID"),
		guestMarkerPath, marker, vmPublicIPLine(), vmSSHKeyLine())
}

// testAccVmUserDataConfigKnownBad guarantees the marker is never written, so cloud-init's
// payload effectively fails to land the marker — yet the VM still reaches RUNNING (the
// backend does not surface cloud-init failures). cloud-init's runcmd has no set -e, so two
// separate items ("exit 1" then "echo marker") would still write the marker; instead we use
// a single shell where "false && echo" short-circuits, so the marker is NEVER written. The
// guest-agent enable still succeeds so first boot completes exactly like the good path,
// keeping VM provisioning parallel to the good config.
func testAccVmUserDataConfigKnownBad(name, marker string) string {
	return fmt.Sprintf(`
locals {
  ud = <<-EOT
    #cloud-config
    runcmd:
      - [ systemctl, enable, --now, qemu-guest-agent ]
      - [ sh, -c, "false && echo %[5]s > %[4]s" ]
  EOT
}

resource "prodata_vm" "test" {
  name             = %[1]q
  image_id         = %[2]s
  cpu_cores        = 1
  ram              = 2
  disk_size        = 20
  disk_type        = "SSD"
  local_network_id = %[3]s
  password         = "AccTestUserData123"
%[6]s%[7]s
  user_data      = local.ud
  user_data_hash = sha256(local.ud)

  timeouts {
    create = "15m"
  }
}
`, name, os.Getenv("PRODATA_VM_TEST_IMAGE_ID"), os.Getenv("PRODATA_VM_TEST_NET_ID"),
		guestMarkerPath, marker, vmPublicIPLine(), vmSSHKeyLine())
}

// testAccVmUserDataConfigInvalid uses a payload that fails the client-side prefix
// validator. Dummy ids keep it independent of test-stand infrastructure.
func testAccVmUserDataConfigInvalid(name string) string {
	return fmt.Sprintf(`
resource "prodata_vm" "test" {
  name             = %[1]q
  image_id         = 1
  cpu_cores        = 1
  ram              = 2
  disk_size        = 20
  disk_type        = "SSD"
  local_network_id = 1
  password         = "AccTestUserData123"
  user_data        = "this is not a cloud-config"
  user_data_hash   = "deadbeef"
}
`, name)
}

// testAccVmUserDataConfigNoHash sets user_data without user_data_hash, which ModifyPlan
// must reject. Dummy ids keep it infrastructure-free.
func testAccVmUserDataConfigNoHash(name string) string {
	return fmt.Sprintf(`
resource "prodata_vm" "test" {
  name             = %[1]q
  image_id         = 1
  cpu_cores        = 1
  ram              = 2
  disk_size        = 20
  disk_type        = "SSD"
  local_network_id = 1
  password         = "AccTestUserData123"
  user_data        = "#cloud-config\npackages: [htop]\n"
}
`, name)
}

// ---- checks ----

// checkCanaryAbsentFromState fails if the raw payload (identified by its unique marker)
// shows up in any attribute of any resource in state — proving user_data stays write-only.
func checkCanaryAbsentFromState(canary string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		for name, rs := range s.RootModule().Resources {
			for k, v := range rs.Primary.Attributes {
				if strings.Contains(v, canary) {
					return fmt.Errorf("write-only payload leaked into state: %s.%s contains the user_data marker %q", name, k, canary)
				}
			}
		}
		return nil
	}
}

// checkGuestMarker SSHes into the VM (when enabled) and asserts the marker is present
// (mustSucceed) or absent (known-bad). It is a no-op when SSH verification is not wired.
func checkGuestMarker(t *testing.T, resourceName, marker string, mustSucceed bool) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		if !sshEnabled(t) {
			t.Logf("[CLOUD-INIT WITNESS SKIPPED] SSH not configured (set PRODATA_VM_TEST_SSH_REACHABLE=1 + key/privkey/public-ip); this run does NOT prove cloud-init executed — only that the VM reached RUNNING.")
			return nil
		}
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource %s not found in state", resourceName)
		}
		host := rs.Primary.Attributes["public_ip"]
		if host == "" {
			return fmt.Errorf("VM has no public_ip; set PRODATA_VM_TEST_PUBLIC_IP_ID so the guest is reachable")
		}
		err := sshMarkerPresent(host, marker)
		if mustSucceed {
			return err
		}
		if err == nil {
			return fmt.Errorf("expected the marker to be ABSENT (known-bad cloud-init) but the witness found it on %s", host)
		}
		return nil
	}
}

// sshMarkerPresent returns nil only if the marker file is present on the guest. It uses
// the system ssh client (no x/crypto dependency), matching the team's playbook helpers.
func sshMarkerPresent(host, marker string) error {
	user := os.Getenv("PRODATA_VM_TEST_SSH_USER")
	if user == "" {
		user = "root"
	}
	addr := net.JoinHostPort(host, "22")
	if c, err := net.DialTimeout("tcp", addr, 10*time.Second); err != nil {
		return fmt.Errorf("guest %s not reachable on 22: %w", host, err)
	} else {
		_ = c.Close()
	}
	out, err := exec.Command("ssh",
		"-i", os.Getenv("PRODATA_VM_TEST_SSH_PRIVKEY"),
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=15",
		"-o", "BatchMode=yes",
		fmt.Sprintf("%s@%s", user, host),
		fmt.Sprintf("sudo cat %s 2>/dev/null || cat %s 2>/dev/null", guestMarkerPath, guestMarkerPath),
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh to %s: %w (output: %s)", host, err, strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), marker) {
		return fmt.Errorf("marker %q not found on guest %s (cloud-init may have failed); got: %q", marker, host, strings.TrimSpace(string(out)))
	}
	return nil
}

// ---- destroy + sweep ----

func vmImportID(resourceName string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("resource %s not found in state", resourceName)
		}
		return rs.Primary.Attributes["id"], nil
	}
}

func testAccCheckVmDestroy(s *terraform.State) error {
	c, err := accClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "prodata_vm" {
			continue
		}
		id, err := strconv.ParseInt(rs.Primary.Attributes["id"], 10, 64)
		if err != nil {
			return fmt.Errorf("parse vm id %q: %w", rs.Primary.Attributes["id"], err)
		}
		opts := &client.RequestOpts{
			Region:     rs.Primary.Attributes["region"],
			ProjectTag: rs.Primary.Attributes["project_tag"],
		}
		_, err = c.GetVm(ctx, id, opts)
		if err != nil {
			if client.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("unexpected error checking destroyed vm %d: %w", id, err)
		}
		return fmt.Errorf("virtual machine %d still exists after destroy", id)
	}
	return nil
}

// sweepVms deletes acceptance VMs left behind by interrupted runs, by name prefix.
func sweepVms(_ string) error {
	c, err := accClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	vms, err := c.GetVms(ctx, nil)
	if err != nil {
		return fmt.Errorf("list vms: %w", err)
	}
	for _, vm := range vms {
		if !strings.HasPrefix(vm.Name, accResourcePrefix) {
			continue
		}
		if derr := c.DeleteVm(ctx, vm.ID, nil); derr != nil && !client.IsNotFound(derr) {
			log.Printf("[WARN] sweep: failed to delete vm %d (%q): %v", vm.ID, vm.Name, derr)
		}
	}
	return nil
}
