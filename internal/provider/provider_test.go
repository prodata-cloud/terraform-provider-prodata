package provider

import (
	"os"
	"strings"
	"testing"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// accResourcePrefix is the disposable name prefix every acceptance-created
// resource carries. Sweepers reap leaks by matching it, and the production
// mutation guard relies on it to keep mutating runs constrained to obviously
// throw-away names. Kept short and lowercase so it is a legal bucket name
// (3-24 chars) and a legal load balancer name in one shape.
const accResourcePrefix = "tfacc-"

// accNameCharset is the lowercase alphanumeric set used for random name suffixes
// (valid for both S3 bucket and load balancer names).
const accNameCharset = "abcdefghijklmnopqrstuvwxyz0123456789"

// accName returns a unique, length-bounded, lowercase name. 18 chars total
// (prefix 6 + 12 random) — within the S3 bucket 24-char limit and the LB 63-char
// limit, with no leading/trailing/consecutive separators.
func accName() string {
	return accResourcePrefix + acctest.RandStringFromCharSet(12, accNameCharset)
}

// testAccProtoV6ProviderFactories wires the in-process provider for acceptance
// tests under the "prodata" name (the canonical terraform-plugin-framework pattern).
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"prodata": providerserver.NewProtocol6WithError(New("test")()),
}

// testAccPreCheck verifies the credentials and scope every acceptance test needs.
// Per HashiCorp's acceptance-test guidance, PreCheck fails fast when a required
// variable is missing rather than letting Terraform surface a confusing error.
func testAccPreCheck(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"PRODATA_API_BASE_URL",
		"PRODATA_API_KEY_ID",
		"PRODATA_API_SECRET_KEY",
		"PRODATA_REGION",
		"PRODATA_PROJECT_TAG",
	} {
		if os.Getenv(k) == "" {
			t.Fatalf("%s must be set for acceptance tests", k)
		}
	}
}

// testAccProdMutationGuard blocks resource-creating acceptance tests against a
// production stand unless the operator explicitly opts in. Disposable test stands
// run freely; a production host requires PRODATA_ACC_ALLOW_PROD_MUTATION=1.
func testAccProdMutationGuard(t *testing.T) {
	t.Helper()
	base := os.Getenv("PRODATA_API_BASE_URL")
	if isProdHost(base) && os.Getenv("PRODATA_ACC_ALLOW_PROD_MUTATION") != "1" {
		t.Skipf("production host %q detected — set PRODATA_ACC_ALLOW_PROD_MUTATION=1 to allow mutating acceptance tests", base)
	}
}

// isProdHost reports whether the API base URL points at a production stand.
// Disposable test stands carry "test" in the hostname; production hosts do not.
func isProdHost(base string) bool {
	host := strings.ToLower(base)
	return strings.Contains(host, "pro-data.tech") && !strings.Contains(host, "test")
}

// accClient builds a client from the same environment the provider reads, for use
// in CheckDestroy assertions and sweepers. The base URL is host-only; client.New
// appends the /panel-main context path itself, exactly like the provider.
func accClient() (*client.Client, error) {
	return client.New(client.Config{
		APIBaseURL:   os.Getenv("PRODATA_API_BASE_URL"),
		APIKeyID:     os.Getenv("PRODATA_API_KEY_ID"),
		APISecretKey: os.Getenv("PRODATA_API_SECRET_KEY"),
		Region:       os.Getenv("PRODATA_REGION"),
		ProjectTag:   os.Getenv("PRODATA_PROJECT_TAG"),
		UserAgent:    "terraform-provider-prodata/acctest",
	})
}

// TestMain enables the sweeper framework (`go test -sweep=...`). Sweepers are
// registered via init() in the per-resource acceptance test files.
func TestMain(m *testing.M) {
	resource.TestMain(m)
}
