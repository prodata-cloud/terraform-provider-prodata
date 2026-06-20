package provider

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"testing"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func init() {
	resource.AddTestSweepers("prodata_kubernetes_cluster", &resource.Sweeper{
		Name: "prodata_kubernetes_cluster",
		F:    sweepK8sClusters,
	})
}

// testAccPreCheckK8s adds an explicit opt-in on top of the standard checks: a
// Kubernetes cluster is an expensive, slow-to-provision multi-VM object, so these
// tests stay off unless PRODATA_K8S_ACC=1 is set deliberately.
func testAccPreCheckK8s(t *testing.T) {
	t.Helper()
	testAccPreCheck(t)
	if os.Getenv("PRODATA_K8S_ACC") != "1" {
		t.Skip("set PRODATA_K8S_ACC=1 to run the (expensive) Kubernetes acceptance tests")
	}
}

// TestAccK8sCluster_basic provisions a minimal single-worker cluster, asserts it
// reaches SUCCESS, and round-trips import (bare-id form).
func TestAccK8sCluster_basic(t *testing.T) {
	name := accName()
	resourceName := "prodata_kubernetes_cluster.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheckK8s(t); testAccProdMutationGuard(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckK8sClusterDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccK8sClusterConfig(name),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("name"), knownvalue.StringExact(name)),
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("status"), knownvalue.StringExact("SUCCESS")),
				},
			},
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: false, // write-once create-time inputs are not read back.
			},
		},
	})
}

// testAccK8sClusterConfig is a minimal, non-HA, single-node cluster on an inline network.
func testAccK8sClusterConfig(name string) string {
	return fmt.Sprintf(`
data "prodata_kubernetes_versions" "v" {}

data "prodata_kubernetes_flavors" "standard" {
  high_availability = false
}

resource "prodata_local_network" "k8s" {
  name    = %[1]q
  cidr    = "10.30.0.0/24"
  gateway = "10.30.0.1"
}

resource "prodata_kubernetes_cluster" "test" {
  name               = %[1]q
  kubernetes_version = data.prodata_kubernetes_versions.v.latest_version
  network_id         = prodata_local_network.k8s.id
  pod_cidr           = "10.244.0.0/16"
  node_subnet        = 24
  node_ip_range      = "10.30.0.10-10.30.0.20"
  master_flavor_id   = data.prodata_kubernetes_flavors.standard.flavors[0].id

  default_node_pool = {
    name       = "workers"
    vcpu       = 2
    ram        = 4
    disk_size  = 40
    node_count = 1
  }

  timeouts = {
    create = "40m"
    delete = "30m"
  }
}
`, name)
}

func testAccCheckK8sClusterDestroy(s *terraform.State) error {
	c, err := accClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "prodata_kubernetes_cluster" {
			continue
		}
		id, err := strconv.ParseInt(rs.Primary.Attributes["id"], 10, 64)
		if err != nil {
			return fmt.Errorf("parse cluster id %q: %w", rs.Primary.Attributes["id"], err)
		}
		opts := &client.RequestOpts{
			Region:     rs.Primary.Attributes["region"],
			ProjectTag: rs.Primary.Attributes["project_tag"],
		}
		cl, err := c.GetCluster(ctx, id, opts)
		if err != nil {
			if client.IsKuberNotFound(err) || client.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("unexpected error checking destroyed cluster %d: %w", id, err)
		}
		if cl.Status == "DELETED" {
			continue
		}
		return fmt.Errorf("cluster %d still exists after destroy (status %q)", id, cl.Status)
	}
	return nil
}

// sweepK8sClusters deletes acceptance clusters left behind by interrupted runs.
func sweepK8sClusters(_ string) error {
	c, err := accClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	clusters, err := c.ListClusters(ctx, nil)
	if err != nil {
		return fmt.Errorf("list clusters: %w", err)
	}
	for _, cl := range clusters {
		if !strings.HasPrefix(cl.Name, accResourcePrefix) || cl.Status == "DELETED" {
			continue
		}
		if derr := c.DeleteCluster(ctx, cl.ID, nil); derr != nil && !client.IsKuberNotFound(derr) {
			log.Printf("[WARN] sweep: failed to delete cluster %d (%q): %v", cl.ID, cl.Name, derr)
		}
	}
	return nil
}
