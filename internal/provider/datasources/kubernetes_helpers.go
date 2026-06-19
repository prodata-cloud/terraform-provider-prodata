package datasources

import (
	"context"
	"strconv"
	"strings"

	"terraform-provider-prodata/internal/client"
	"terraform-provider-prodata/internal/tfutil"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// K8sKubeConfigModel is the typed source for the computed kube_config object of
// the cluster data source: connection fields parsed from the kubeconfig for
// wiring the kubernetes / helm providers. Mirrors the resource's block.
type K8sKubeConfigModel struct {
	Host                 types.String `tfsdk:"host"`
	ClusterCACertificate types.String `tfsdk:"cluster_ca_certificate"`
	ClientCertificate    types.String `tfsdk:"client_certificate"`
	ClientKey            types.String `tfsdk:"client_key"`
	Token                types.String `tfsdk:"token"`
	RawConfig            types.String `tfsdk:"raw_config"`
}

// kubeConfigAttrTypes is the object type of the kube_config block. It must stay in
// lockstep with K8sKubeConfigModel's tags and the schema (asserted by the
// schema-consistency tests).
func kubeConfigAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"host":                   types.StringType,
		"cluster_ca_certificate": types.StringType,
		"client_certificate":     types.StringType,
		"client_key":             types.StringType,
		"token":                  types.StringType,
		"raw_config":             types.StringType,
	}
}

// kubeConfigObject parses the base64 kubeconfig secret into the computed
// kube_config object, or a null object when the cluster has no kubeconfig yet.
func kubeConfigObject(ctx context.Context, secret string) types.Object {
	kc := client.ParseKubeConfig(secret)
	if kc == nil {
		return types.ObjectNull(kubeConfigAttrTypes())
	}
	obj, diags := types.ObjectValueFrom(ctx, kubeConfigAttrTypes(), K8sKubeConfigModel{
		Host:                 tfutil.StringOrNull(kc.Host),
		ClusterCACertificate: tfutil.StringOrNull(kc.ClusterCACertificate),
		ClientCertificate:    tfutil.StringOrNull(kc.ClientCertificate),
		ClientKey:            tfutil.StringOrNull(kc.ClientKey),
		Token:                tfutil.StringOrNull(kc.Token),
		RawConfig:            tfutil.StringOrNull(kc.Raw),
	})
	if diags.HasError() {
		return types.ObjectNull(kubeConfigAttrTypes())
	}
	return obj
}

// scopeOpts builds a RequestOpts carrying only the region / project_tag overrides
// that are actually set; an empty field defers to the provider/client default.
func scopeOpts(region, projectTag types.String) *client.RequestOpts {
	opts := &client.RequestOpts{}
	if !region.IsNull() && !region.IsUnknown() && region.ValueString() != "" {
		opts.Region = region.ValueString()
	}
	if !projectTag.IsNull() && !projectTag.IsUnknown() && projectTag.ValueString() != "" {
		opts.ProjectTag = projectTag.ValueString()
	}
	return opts
}

// compareK8sVersion orders two Kubernetes version strings (e.g. "v1.31.4") by their
// numeric major.minor.patch components: it returns -1 if a < b, 0 if equal, 1 if
// a > b. A missing or non-numeric component sorts as 0, so a malformed version
// never sorts above a well-formed one of the same prefix. This is a small, self-
// contained comparator (the versions are plain release tags, not full semver with
// pre-release/build metadata), avoiding a semver dependency. A pre-release suffix
// (e.g. "-rc1") is ignored, so "v1.31.4-rc1" compares EQUAL to "v1.31.4"; this is
// acceptable only because the ProData version catalog carries plain release tags.
func compareK8sVersion(a, b string) int {
	av := parseK8sVersion(a)
	bv := parseK8sVersion(b)
	for i := 0; i < 3; i++ {
		if av[i] < bv[i] {
			return -1
		}
		if av[i] > bv[i] {
			return 1
		}
	}
	return 0
}

// parseK8sVersion extracts up to three numeric components from a "vX.Y.Z" string.
// Non-numeric or missing components are 0.
func parseK8sVersion(v string) [3]int {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	for i, part := range strings.SplitN(v, ".", 3) {
		if i >= 3 {
			break
		}
		// Stop at the first non-digit run (handles a trailing "-rc1" etc.).
		end := 0
		for end < len(part) && part[end] >= '0' && part[end] <= '9' {
			end++
		}
		if end > 0 {
			if n, err := strconv.Atoi(part[:end]); err == nil {
				out[i] = n
			}
		}
	}
	return out
}
