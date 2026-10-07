package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// TestAccLocalNetworkDataSource_byName creates a network, then looks it up by name and by
// id and expects the same network back from both. It also checks that a name nobody owns is
// reported as an error rather than an empty result.
func TestAccLocalNetworkDataSource_byName(t *testing.T) {
	name := accName() + "-lookup"

	config := fmt.Sprintf(`
resource "prodata_local_network" "test" {
  name    = %[1]q
  cidr    = "10.52.0.0/24"
  gateway = "10.52.0.1"
}

data "prodata_local_network" "by_name" {
  name = prodata_local_network.test.name
}

data "prodata_local_network" "by_id" {
  id = prodata_local_network.test.id
}
`, name)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t); testAccProdMutationGuard(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckLocalNetworkDestroy,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.CompareValuePairs(
						"data.prodata_local_network.by_name", tfjsonpath.New("id"),
						"prodata_local_network.test", tfjsonpath.New("id"),
						compare.ValuesSame(),
					),
					statecheck.ExpectKnownValue("data.prodata_local_network.by_name", tfjsonpath.New("name"), knownvalue.StringExact(name)),
					statecheck.ExpectKnownValue("data.prodata_local_network.by_name", tfjsonpath.New("cidr"), knownvalue.StringExact("10.52.0.0/24")),
					statecheck.ExpectKnownValue("data.prodata_local_network.by_name", tfjsonpath.New("gateway"), knownvalue.StringExact("10.52.0.1")),
					statecheck.ExpectKnownValue("data.prodata_local_network.by_id", tfjsonpath.New("name"), knownvalue.StringExact(name)),
				},
			},
			{
				Config: config + fmt.Sprintf(`
data "prodata_local_network" "missing" {
  name = %q
}
`, name+"-does-not-exist"),
				ExpectError: regexp.MustCompile(`(?i)No local network named`),
			},
		},
	})
}
