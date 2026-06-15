package datasources

import (
	"strconv"
	"strings"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

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
