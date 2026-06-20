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
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func init() {
	resource.AddTestSweepers("prodata_kubernetes_node_pool", &resource.Sweeper{
		Name: "prodata_kubernetes_node_pool",
		F:    sweepK8sNodePools,
	})
}

// TestAccK8sNodePool_basic provisions a cluster plus an additional fixed-size worker
// pool, asserts the pool reaches SUCCESS, and round-trips import ("cluster_id/pool_id").
// Gated by PRODATA_K8S_ACC=1 (it provisions a full cluster).
func TestAccK8sNodePool_basic(t *testing.T) {
	name := accName()
	resourceName := "prodata_kubernetes_node_pool.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheckK8s(t); testAccProdMutationGuard(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckK8sNodePoolDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccK8sNodePoolConfig(name),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("name"), knownvalue.StringExact("extra")),
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("status"), knownvalue.StringExact("SUCCESS")),
				},
			},
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: false,
				ImportStateIdFunc: k8sNodePoolImportID(resourceName),
			},
		},
	})
}

func testAccK8sNodePoolConfig(name string) string {
	return testAccK8sClusterConfig(name) + `
resource "prodata_kubernetes_node_pool" "test" {
  cluster_id = prodata_kubernetes_cluster.test.id
  name       = "extra"
  vcpu       = 2
  ram        = 4
  disk_size  = 40
  node_count = 1
}
`
}

func k8sNodePoolImportID(resourceName string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("resource %s not found in state", resourceName)
		}
		a := rs.Primary.Attributes
		return fmt.Sprintf("%s/%s", a["cluster_id"], a["id"]), nil
	}
}

func testAccCheckK8sNodePoolDestroy(s *terraform.State) error {
	c, err := accClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "prodata_kubernetes_node_pool" {
			continue
		}
		id, err := strconv.ParseInt(rs.Primary.Attributes["id"], 10, 64)
		if err != nil {
			return fmt.Errorf("parse node pool id %q: %w", rs.Primary.Attributes["id"], err)
		}
		opts := &client.RequestOpts{
			Region:     rs.Primary.Attributes["region"],
			ProjectTag: rs.Primary.Attributes["project_tag"],
		}
		_, err = c.GetNodePool(ctx, id, opts)
		if err != nil {
			if client.IsKuberNotFound(err) || client.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("unexpected error checking destroyed node pool %d: %w", id, err)
		}
		return fmt.Errorf("node pool %d still exists after destroy", id)
	}
	return nil
}

// sweepK8sNodePools deletes acceptance node pools left behind by interrupted runs by
// walking acceptance clusters. (The cluster sweeper removes pools with their cluster;
// this catches pools whose cluster was kept or swept separately.)
func sweepK8sNodePools(_ string) error {
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
		pools, perr := c.ListNodePools(ctx, cl.ID, nil)
		if perr != nil {
			log.Printf("[WARN] sweep: list node pools for cluster %d: %v", cl.ID, perr)
			continue
		}
		for _, p := range pools {
			if derr := c.DeleteNodePool(ctx, p.ID, nil); derr != nil && !client.IsKuberNotFound(derr) {
				log.Printf("[WARN] sweep: failed to delete node pool %d (cluster %d): %v", p.ID, cl.ID, derr)
			}
		}
	}
	return nil
}
