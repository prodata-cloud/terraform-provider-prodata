package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccK8sCluster_nodeIPRangePreflight exercises the plan-time node_ip_range checks
// against a real local network without ever creating a cluster: every cluster step is
// PlanOnly, so the only thing created (and destroyed) is the network. The network is
// found through the name lookup of the prodata_local_network data source.
func TestAccK8sCluster_nodeIPRangePreflight(t *testing.T) {
	name := accName() + "-nir"

	network := fmt.Sprintf(`
resource "prodata_local_network" "k8s" {
  name    = %[1]q
  cidr    = "10.54.1.0/24"
  gateway = "10.54.1.1"
}
`, name)

	// The cluster is only ever planned. network_id comes from the data source, so it is
	// known at plan time once the network exists.
	cluster := func(nodeIPRange string) string {
		return network + fmt.Sprintf(`
data "prodata_local_network" "by_name" {
  name = prodata_local_network.k8s.name
}

resource "prodata_kubernetes_cluster" "test" {
  name               = %[1]q
  kubernetes_version = "v1.31.4"
  network_id         = data.prodata_local_network.by_name.id
  pod_cidr           = "10.244.0.0/16"
  node_ip_range      = %[2]q
  master_flavor_id   = 1
}
`, name, nodeIPRange)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t); testAccProdMutationGuard(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckLocalNetworkDestroy,
		Steps: []resource.TestStep{
			{Config: network},
			{
				// gateway 10.54.1.1 sits inside the range
				Config:      cluster("10.54.1.0-10.54.1.20"),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`Invalid node_ip_range for network_id`),
			},
			{
				// a range in some other network
				Config:      cluster("10.99.9.10-10.99.9.20"),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`Invalid node_ip_range for network_id`),
			},
			{
				// reversed: rejected by the static validator, not by the network check
				Config:      cluster("10.54.1.20-10.54.1.10"),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`range is reversed`),
			},
			{
				// a good range plans cleanly (a cluster would be created, hence non-empty)
				Config:             cluster("10.54.1.10-10.54.1.20"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}
