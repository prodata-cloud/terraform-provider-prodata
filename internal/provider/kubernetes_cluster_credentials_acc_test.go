package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/echoprovider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
)

// Acceptance tests of the two ways a cluster's credentials are kept out of Terraform state:
// the exclude_credentials_from_state opt-out on the cluster resource and data source, and the
// prodata_kubernetes_kubeconfig ephemeral resource that serves the same values for one run.
//
// Both need Terraform 1.10 or later (ephemeral resources) and are skipped below it.
//
//   - TestAccK8sKubeconfig_clusterNotFound only reads, creates nothing and needs nothing beyond
//     the usual PRODATA_* variables, so it also runs on a stand that cannot provision clusters.
//   - TestAccK8sCluster_credentialsOutOfState provisions a real cluster and is gated by
//     PRODATA_K8S_ACC=1 like the other cluster tests.

const (
	accK8sCredsCluster = "prodata_kubernetes_cluster.test"
	accK8sCredsData    = "data.prodata_kubernetes_cluster.by_name"
	accK8sCredsEcho    = "echo.creds"
)

// testAccProtoV6ProviderFactoriesWithEcho adds the "echo" provider to the usual factories. An
// ephemeral value never reaches state, so a test can only look at it by handing it to echo:
// its provider configuration is ephemeral too, and its one resource copies that value into
// state.
var testAccProtoV6ProviderFactoriesWithEcho = map[string]func() (tfprotov6.ProviderServer, error){
	"prodata": testAccProtoV6ProviderFactories["prodata"],
	"echo":    echoprovider.NewProviderServer(),
}

// TestAccK8sKubeconfig_clusterNotFound asks for the kubeconfig of a cluster that does not
// exist. It shows that the ephemeral resource is registered, is handed the provider's
// credentials and scope, and fails rather than returning empty values — the failure the
// kubernetes and helm providers configured from it depend on. Read-only: no mutation guard.
func TestAccK8sKubeconfig_clusterNotFound(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_10_0)},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactoriesWithEcho,
		Steps: []resource.TestStep{
			{
				Config: `
ephemeral "prodata_kubernetes_kubeconfig" "missing" {
  cluster_id = 999999999
}

provider "echo" {
  data = ephemeral.prodata_kubernetes_kubeconfig.missing.host
}

resource "echo" "this" {}
`,
				ExpectError: regexp.MustCompile(`Kubernetes cluster not found`),
			},
		},
	})
}

// TestAccK8sCluster_credentialsOutOfState follows one real cluster through the opt-out:
//
//  1. Without it the cluster is created. The panel generates the SSH key pair when the cluster is
//     created, so private_key_encoded is stored at once.
//  2. The same configuration again. A cluster can be ready before the panel has produced its
//     kubeconfig, which only a refresh then stores, so the test records the credentials here.
//  3. Turning the opt-out on is an in-place update that plans and stores null for kube_config
//     and private_key_encoded; the recorded credentials are nowhere in the state or in a plan.
//  4. With it on, a data source with the opt-out also returns null, and the ephemeral resource
//     still serves the connection details for the run (read through the echo provider, which
//     is given only the host and yes/no answers, never a secret).
//  5. Turning it off again is another in-place update that puts the credentials back, and the
//     stored host equals the one the ephemeral resource served in step 4.
//  6. The data source and the ephemeral resource are removed again, so that the destroy after
//     the test does not have to open the ephemeral resource: if that failed, the cluster and
//     its network would be left behind.
//
// The cluster has SSH access with a platform-generated key pair so that private_key_encoded
// exists to be kept out. Gated by PRODATA_K8S_ACC=1 (it provisions a full cluster).
//
// If a step fails while the readers (steps 4 and 5) are in the configuration, the destroy that
// follows has to open the ephemeral resource too and may fail with it. `make sweep` then asks for
// the cluster to be deleted but does not wait for that, so the network goes only with a second
// run, once the cluster is gone.
func TestAccK8sCluster_credentialsOutOfState(t *testing.T) {
	name := accName()
	watch := &credentialWatch{}
	hostsAgree := statecheck.CompareValue(compare.ValuesSame())

	// The plans Terraform makes after an apply with the opt-out on, which carry the prior
	// state and the planned values, must not hold the credentials either.
	withoutCredentials := resource.ConfigPlanChecks{
		PostApplyPreRefresh:  []plancheck.PlanCheck{watch.absentFromPlan()},
		PostApplyPostRefresh: []plancheck.PlanCheck{watch.absentFromPlan()},
	}
	inPlace := []plancheck.PlanCheck{
		plancheck.ExpectResourceAction(accK8sCredsCluster, plancheck.ResourceActionUpdate),
	}
	optOutOn := resource.ConfigPlanChecks{
		PreApply: append(append([]plancheck.PlanCheck{}, inPlace...),
			plancheck.ExpectKnownValue(accK8sCredsCluster, tfjsonpath.New("kube_config"), knownvalue.Null()),
			plancheck.ExpectKnownValue(accK8sCredsCluster, tfjsonpath.New("private_key_encoded"), knownvalue.Null()),
		),
		PostApplyPreRefresh:  withoutCredentials.PostApplyPreRefresh,
		PostApplyPostRefresh: withoutCredentials.PostApplyPostRefresh,
	}
	privateKeyKept := statecheck.ExpectKnownValue(accK8sCredsCluster, tfjsonpath.New("private_key_encoded"), knownvalue.NotNull())
	credentialsKept := []statecheck.StateCheck{
		statecheck.ExpectKnownValue(accK8sCredsCluster, tfjsonpath.New("kube_config").AtMapKey("host"), knownvalue.NotNull()),
		privateKeyKept,
	}
	credentialsOut := func(address string) []statecheck.StateCheck {
		return []statecheck.StateCheck{
			statecheck.ExpectKnownValue(address, tfjsonpath.New("kube_config"), knownvalue.Null()),
			statecheck.ExpectKnownValue(address, tfjsonpath.New("private_key_encoded"), knownvalue.Null()),
		}
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheckK8s(t); testAccProdMutationGuard(t) },
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_10_0)},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactoriesWithEcho,
		CheckDestroy: resource.ComposeAggregateTestCheckFunc(
			testAccCheckK8sClusterDestroy,
			testAccCheckLocalNetworkDestroy,
		),
		Steps: []resource.TestStep{
			{
				// 1. No opt-out: the cluster is created, and the SSH key is stored straight away.
				Config: testAccK8sCredentialsConfig(name, false, false),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(accK8sCredsCluster, tfjsonpath.New("status"), knownvalue.StringExact("SUCCESS")),
					statecheck.ExpectKnownValue(accK8sCredsCluster, tfjsonpath.New("exclude_credentials_from_state"), knownvalue.Null()),
					privateKeyKept,
				},
			},
			{
				// 2. The same configuration. Its plan refreshes the cluster first, which stores a
				// kubeconfig the panel produced only after the cluster became ready; now the
				// credentials can be recorded.
				Config:            testAccK8sCredentialsConfig(name, false, false),
				ConfigStateChecks: append([]statecheck.StateCheck{watch.learn(accK8sCredsCluster)}, credentialsKept...),
			},
			{
				// 3. Opt-out on: an in-place update that empties both attributes.
				Config:            testAccK8sCredentialsConfig(name, true, false),
				ConfigPlanChecks:  optOutOn,
				ConfigStateChecks: append(credentialsOut(accK8sCredsCluster), watch.absentFromState()),
			},
			{
				// 4. Data source with the opt-out, and the ephemeral resource.
				Config:           testAccK8sCredentialsConfig(name, true, true),
				ConfigPlanChecks: withoutCredentials,
				ConfigStateChecks: append(append(credentialsOut(accK8sCredsCluster), credentialsOut(accK8sCredsData)...),
					statecheck.ExpectKnownValue(accK8sCredsEcho, tfjsonpath.New("data").AtMapKey("host"), knownvalue.StringRegexp(regexp.MustCompile(`^https?://.+`))),
					statecheck.ExpectKnownValue(accK8sCredsEcho, tfjsonpath.New("data").AtMapKey("has_ca"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue(accK8sCredsEcho, tfjsonpath.New("data").AtMapKey("has_credentials"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue(accK8sCredsEcho, tfjsonpath.New("data").AtMapKey("has_raw_config"), knownvalue.Bool(true)),
					watch.absentFromState(),
				),
			},
			{
				// 5. Opt-out off again: the credentials return. The echo resource keeps what it
				// was given in step 4 (it never changes after it is created), so the host compared
				// here is the one the ephemeral resource served then.
				Config:           testAccK8sCredentialsConfig(name, false, true),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: inPlace},
				ConfigStateChecks: append(append([]statecheck.StateCheck{}, credentialsKept...),
					statecheck.ExpectKnownValue(accK8sCredsData, tfjsonpath.New("kube_config"), knownvalue.Null()),
					hostsAgree.AddStateValue(accK8sCredsCluster, tfjsonpath.New("kube_config").AtMapKey("host")),
					hostsAgree.AddStateValue(accK8sCredsEcho, tfjsonpath.New("data").AtMapKey("host")),
				),
			},
			{
				// 6. Without the readers. The echo resource is deleted with them, so the destroy
				// after the test no longer configures a provider from the ephemeral resource.
				Config: testAccK8sCredentialsConfig(name, false, false),
			},
		},
	})
}

// testAccK8sCredentialsConfig is a minimal single-node cluster on its own network, with SSH
// access so that the platform generates a key pair. optOut sets exclude_credentials_from_state
// on the cluster; readers adds a data source with the opt-out and the ephemeral resource, whose
// values are handed to the echo provider as the host plus yes/no answers.
func testAccK8sCredentialsConfig(name string, optOut, readers bool) string {
	exclude := ""
	if optOut {
		exclude = "\n  exclude_credentials_from_state = true"
	}
	cfg := fmt.Sprintf(`
data "prodata_kubernetes_versions" "v" {}

data "prodata_kubernetes_flavors" "standard" {
  high_availability = false
}

resource "prodata_local_network" "k8s" {
  name    = %[1]q
  cidr    = "10.54.2.0/24"
  gateway = "10.54.2.1"
}

resource "prodata_kubernetes_cluster" "test" {
  name               = %[1]q
  kubernetes_version = data.prodata_kubernetes_versions.v.latest_version
  network_id         = prodata_local_network.k8s.id
  pod_cidr           = "10.244.0.0/16"
  node_ip_range      = "10.54.2.10-10.54.2.20"
  master_flavor_id   = data.prodata_kubernetes_flavors.standard.flavors[0].id
  ssh_access_enabled = true%[2]s

  timeouts = {
    create = "40m"
    delete = "30m"
  }
}
`, name, exclude)
	if !readers {
		return cfg
	}
	return cfg + `
data "prodata_kubernetes_cluster" "by_name" {
  name                           = prodata_kubernetes_cluster.test.name
  exclude_credentials_from_state = true
}

ephemeral "prodata_kubernetes_kubeconfig" "this" {
  cluster_id = prodata_kubernetes_cluster.test.id
}

provider "echo" {
  data = {
    host            = ephemeral.prodata_kubernetes_kubeconfig.this.host
    has_ca          = ephemeral.prodata_kubernetes_kubeconfig.this.cluster_ca_certificate != null
    has_credentials = (ephemeral.prodata_kubernetes_kubeconfig.this.client_certificate != null && ephemeral.prodata_kubernetes_kubeconfig.this.client_key != null) || ephemeral.prodata_kubernetes_kubeconfig.this.token != null
    has_raw_config  = ephemeral.prodata_kubernetes_kubeconfig.this.raw_config != null
  }
}

resource "echo" "creds" {}
`
}

// credentialWatch learns the credentials a cluster returns while they may still be stored,
// then looks for each of them, whole, anywhere in later states and plans. Nothing it holds is
// ever printed: a failure names the credential, not its value.
type credentialWatch struct {
	secrets map[string]string
}

// minWatchedLen keeps a short value from matching by accident: a real credential is far longer.
const minWatchedLen = 16

// accStateCheck and accPlanCheck adapt a function to the interfaces the test harness takes.
type (
	accStateCheck func(ctx context.Context, req statecheck.CheckStateRequest, resp *statecheck.CheckStateResponse)
	accPlanCheck  func(ctx context.Context, req plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse)
)

func (f accStateCheck) CheckState(ctx context.Context, req statecheck.CheckStateRequest, resp *statecheck.CheckStateResponse) {
	f(ctx, req, resp)
}

func (f accPlanCheck) CheckPlan(ctx context.Context, req plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
	f(ctx, req, resp)
}

// learn records the credentials of the cluster at address, and the other shapes they come in
// (see withDerivedForms). It insists on finding both a kubeconfig credential and the SSH private
// key, so that the later "absent" checks cannot pass merely because there was nothing to look for.
func (w *credentialWatch) learn(address string) statecheck.StateCheck {
	return accStateCheck(func(_ context.Context, req statecheck.CheckStateRequest, resp *statecheck.CheckStateResponse) {
		attrs, err := accStateAttributes(req, address)
		if err != nil {
			resp.Error = err
			return
		}
		found := map[string]string{}
		if kc, ok := attrs["kube_config"].(map[string]any); ok {
			for _, k := range []string{"client_certificate", "client_key", "token", "raw_config"} {
				if v, _ := kc[k].(string); len(v) >= minWatchedLen {
					found["kube_config."+k] = v
				}
			}
		}
		if v, _ := attrs["private_key_encoded"].(string); len(v) >= minWatchedLen {
			found["private_key_encoded"] = v
		}
		hasKubeconfig := false
		for label := range found {
			hasKubeconfig = hasKubeconfig || strings.HasPrefix(label, "kube_config.")
		}
		if !hasKubeconfig || found["private_key_encoded"] == "" {
			resp.Error = fmt.Errorf("the cluster did not return both a kubeconfig credential and the generated SSH private key "+
				"(found: %s), so there is nothing to look for in the later steps", strings.Join(sortedKeys(found), ", "))
			return
		}
		w.secrets = withDerivedForms(found)
	})
}

// withDerivedForms adds the shapes a credential can take on its way from the panel into state.
// The panel hands the whole kubeconfig over as base64 of its YAML, which the provider stores
// decoded as raw_config; the client certificate and key inside it are base64 of PEM, which the
// provider stores as they come; the SSH key is PEM text that the documentation calls base64.
// A copy in any of these shapes is as much a leak as the stored value, and none of them contains
// it as a substring. So each credential is also looked for base64-encoded and, where it is
// base64 of text, decoded.
func withDerivedForms(found map[string]string) map[string]string {
	all := make(map[string]string, 3*len(found))
	for label, v := range found {
		all[label] = v
		all[label+" (base64)"] = base64.StdEncoding.EncodeToString([]byte(v))
		if dec, err := base64.StdEncoding.DecodeString(v); err == nil && len(dec) >= minWatchedLen && utf8.Valid(dec) {
			all[label+" (decoded)"] = string(dec)
		}
	}
	return all
}

// absentFromState fails when any learned credential appears anywhere in the state.
func (w *credentialWatch) absentFromState() statecheck.StateCheck {
	return accStateCheck(func(_ context.Context, req statecheck.CheckStateRequest, resp *statecheck.CheckStateResponse) {
		leaked, err := w.leaks(req.State)
		switch {
		case err != nil:
			resp.Error = err
		case len(leaked) > 0:
			resp.Error = fmt.Errorf("the state holds credentials of the cluster: %s", strings.Join(leaked, ", "))
		}
	})
}

// absentFromPlan fails when any learned credential appears anywhere in a plan, which also
// holds the prior state and the planned values.
func (w *credentialWatch) absentFromPlan() plancheck.PlanCheck {
	return accPlanCheck(func(_ context.Context, req plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
		leaked, err := w.leaks(req.Plan)
		switch {
		case err != nil:
			resp.Error = err
		case len(leaked) > 0:
			resp.Error = fmt.Errorf("the plan holds credentials of the cluster: %s", strings.Join(leaked, ", "))
		}
	})
}

// leaks returns the labels of the learned credentials found in the JSON form of v. The search
// is over the whole document, so it does not depend on where in the state or plan a credential
// would show up; the needles are escaped the way the document is.
func (w *credentialWatch) leaks(v any) ([]string, error) {
	if len(w.secrets) == 0 {
		return nil, errors.New("no credentials were learned in an earlier step, so there is nothing to look for")
	}
	doc, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode for the credential search: %w", err)
	}
	var leaked []string
	for label, secret := range w.secrets {
		needle, err := json.Marshal(secret)
		if err != nil {
			return nil, fmt.Errorf("encode %s for the credential search: %w", label, err)
		}
		if strings.Contains(string(doc), string(needle[1:len(needle)-1])) {
			leaked = append(leaked, label)
		}
	}
	sort.Strings(leaked)
	return leaked, nil
}

// accStateAttributes returns the attribute values of the root-module resource at address.
func accStateAttributes(req statecheck.CheckStateRequest, address string) (map[string]any, error) {
	if req.State == nil || req.State.Values == nil || req.State.Values.RootModule == nil {
		return nil, errors.New("the state holds no values")
	}
	for _, r := range req.State.Values.RootModule.Resources {
		if r.Address == address {
			return r.AttributeValues, nil
		}
	}
	return nil, fmt.Errorf("%s is not in the state", address)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
