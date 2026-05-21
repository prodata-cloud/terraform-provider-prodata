package provider

import (
	"context"
	"fmt"
	"log"
	"strings"
	"testing"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func init() {
	resource.AddTestSweepers("prodata_s3_bucket", &resource.Sweeper{
		Name: "prodata_s3_bucket",
		F:    sweepS3Buckets,
	})
}

// TestAccS3Bucket_basic exercises the full prodata_s3_bucket lifecycle through the
// Terraform runtime: create+read, an in-place update (enable versioning), a data
// source read-back, and an import round-trip. Each apply asserts plan stability
// (empty plan after apply). Import ignores `acl`, which is trust-state and does not
// round-trip through S3 grants (see the resource's schema notes).
func TestAccS3Bucket_basic(t *testing.T) {
	name := accName()
	resourceName := "prodata_s3_bucket.test"
	dataName := "data.prodata_s3_bucket.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t); testAccProdMutationGuard(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckS3BucketDestroy,
		Steps: []resource.TestStep{
			{ // Create + Read.
				Config: testAccS3BucketConfig(name, "private", false),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("id"), knownvalue.StringExact(name)),
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("name"), knownvalue.StringExact(name)),
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("acl"), knownvalue.StringExact("private")),
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("versioning"), knownvalue.Bool(false)),
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("object_lock_enabled"), knownvalue.Bool(false)),
				},
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{ // Update: enable versioning in place.
				Config: testAccS3BucketConfig(name, "private", true),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(resourceName, tfjsonpath.New("versioning"), knownvalue.Bool(true)),
				},
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{ // Data source read-back of the same bucket.
				Config: testAccS3BucketConfigWithData(name, "private", true),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(dataName, tfjsonpath.New("name"), knownvalue.StringExact(name)),
					statecheck.ExpectKnownValue(dataName, tfjsonpath.New("versioning"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue(dataName, tfjsonpath.New("object_lock_enabled"), knownvalue.Bool(false)),
				},
			},
			{ // Import round-trip.
				ResourceName:            resourceName,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"acl"},
				ImportStateIdFunc:       s3BucketImportID(resourceName),
			},
		},
	})
}

func testAccS3BucketConfig(name, acl string, versioning bool) string {
	return fmt.Sprintf(`
resource "prodata_s3_bucket" "test" {
  name       = %[1]q
  acl        = %[2]q
  versioning = %[3]t
}
`, name, acl, versioning)
}

func testAccS3BucketConfigWithData(name, acl string, versioning bool) string {
	return testAccS3BucketConfig(name, acl, versioning) + `
data "prodata_s3_bucket" "test" {
  name = prodata_s3_bucket.test.name
}
`
}

// s3BucketImportID builds the {region}/{name}@{project_tag} import id from state.
func s3BucketImportID(resourceName string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("resource %s not found in state", resourceName)
		}
		a := rs.Primary.Attributes
		return fmt.Sprintf("%s/%s@%s", a["region"], a["name"], a["project_tag"]), nil
	}
}

func testAccCheckS3BucketDestroy(s *terraform.State) error {
	c, err := accClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "prodata_s3_bucket" {
			continue
		}
		name := rs.Primary.Attributes["name"]
		opts := &client.RequestOpts{
			Region:     rs.Primary.Attributes["region"],
			ProjectTag: rs.Primary.Attributes["project_tag"],
		}
		_, err := c.GetBucket(ctx, name, opts)
		if err == nil {
			return fmt.Errorf("bucket %q still exists after destroy", name)
		}
		if !client.IsNotFound(err) {
			return fmt.Errorf("unexpected error checking destroyed bucket %q: %w", name, err)
		}
	}
	return nil
}

// sweepS3Buckets deletes acceptance-test buckets left behind by interrupted runs.
// It only touches buckets whose name carries the disposable acceptance prefix.
func sweepS3Buckets(_ string) error {
	c, err := accClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	buckets, err := c.ListBuckets(ctx, 0, nil)
	if err != nil {
		return fmt.Errorf("list buckets: %w", err)
	}
	for _, b := range buckets {
		if !strings.HasPrefix(b.Name, accResourcePrefix) {
			continue
		}
		if err := c.DeleteBucket(ctx, b.Name, nil); err != nil && !client.IsNotFound(err) {
			log.Printf("[WARN] sweep: failed to delete bucket %q: %v", b.Name, err)
		}
	}
	return nil
}
