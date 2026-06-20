package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccVm_invalidValuesRejectedAtPlan proves the prodata_vm schema validators reject
// out-of-contract input at plan time (before any API call), instead of letting it fail
// later at apply with an opaque backend error. PlanOnly + dummy ids: validation fails
// first, so no infrastructure is touched.
func TestAccVm_invalidValuesRejectedAtPlan(t *testing.T) {
	cases := []struct {
		name   string
		config string
		errRe  string
	}{
		{"disk below minimum", vmValidatorConfig("web1", 1, 1, 5), `(?i)at least 10`},
		{"cpu below minimum", vmValidatorConfig("web1", 0, 1, 10), `(?i)at least 1`},
		{"name with invalid char", vmValidatorConfig("web_server", 1, 1, 10), `(?i)letters, digits and hyphens`},
		{"name too short", vmValidatorConfig("ab", 1, 1, 10), `(?i)between 3 and 63`},
		{"name without a letter", vmValidatorConfig("123-456", 1, 1, 10), `(?i)at least\s+one letter`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resource.Test(t, resource.TestCase{
				PreCheck:                 func() { testAccPreCheck(t) },
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{
						Config:      tc.config,
						PlanOnly:    true,
						ExpectError: regexp.MustCompile(tc.errRe),
					},
				},
			})
		})
	}
}

func vmValidatorConfig(name string, cpu, ram, disk int64) string {
	return fmt.Sprintf(`
resource "prodata_vm" "test" {
  name             = %q
  image_id         = 1
  cpu_cores        = %d
  ram              = %d
  disk_size        = %d
  disk_type        = "SSD"
  local_network_id = 1
  password         = "ValidatorTest123"
}
`, name, cpu, ram, disk)
}
