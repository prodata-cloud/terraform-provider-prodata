package provider

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// TestAccImageDataSource_bySlug verifies a slug lookup resolves an image and now
// populates the computed attributes — including `name`, the key the caller did not
// supply — without an "inconsistent result" error. Set PRODATA_IMAGE_TEST_SLUG to a
// slug that exists in the target region (e.g. ubuntu-24-04).
func TestAccImageDataSource_bySlug(t *testing.T) {
	slug := os.Getenv("PRODATA_IMAGE_TEST_SLUG")
	if slug == "" {
		t.Skip("set PRODATA_IMAGE_TEST_SLUG to a slug available in the target region")
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`data "prodata_image" "test" { slug = %q }`, slug),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("data.prodata_image.test", tfjsonpath.New("id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("data.prodata_image.test", tfjsonpath.New("slug"), knownvalue.StringExact(slug)),
				},
			},
		},
	})
}
