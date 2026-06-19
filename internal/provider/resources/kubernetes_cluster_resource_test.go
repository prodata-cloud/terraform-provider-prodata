package resources

import (
	"testing"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestParseK8sImportID(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		wantID      int64
		wantRegion  string
		wantProject string
		wantErr     bool
	}{
		{"bare id", "42", 42, "", "", false},
		{"composite", "UZ-5/42@my-project", 42, "UZ-5", "my-project", false},
		{"composite with hyphens in project", "KZ-1/7@team-prod-1", 7, "KZ-1", "team-prod-1", false},
		{"not an integer", "abc", 0, "", "", true},
		{"missing region", "/42@p", 0, "", "", true},
		{"missing project", "UZ-5/42@", 0, "", "", true},
		{"missing id segment", "UZ-5/@p", 0, "", "", true},
		{"composite non-int id", "UZ-5/x@p", 0, "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, region, project, err := parseK8sImportID(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got id=%d region=%q project=%q", tc.in, id, region, project)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if id != tc.wantID || region != tc.wantRegion || project != tc.wantProject {
				t.Errorf("got (%d,%q,%q), want (%d,%q,%q)", id, region, project, tc.wantID, tc.wantRegion, tc.wantProject)
			}
		})
	}
}

func TestDefaultPoolChanged(t *testing.T) {
	fixed := func(n int64) *K8sDefaultPoolModel {
		return &K8sDefaultPoolModel{NodeCount: types.Int64Value(n)}
	}
	auto := func(min, max int64) *K8sDefaultPoolModel {
		return &K8sDefaultPoolModel{
			NodeCount:   types.Int64Null(),
			Autoscaling: &K8sAutoscalingModel{MinNodes: types.Int64Value(min), MaxNodes: types.Int64Value(max)},
		}
	}

	cases := []struct {
		name        string
		state, plan *K8sDefaultPoolModel
		wantChanged bool
	}{
		{"identical fixed", fixed(3), fixed(3), false},
		{"node_count changed", fixed(3), fixed(5), true},
		{"fixed to auto", fixed(3), auto(2, 5), true},
		{"auto to fixed", auto(2, 5), fixed(3), true},
		{"auto bounds changed", auto(2, 5), auto(2, 8), true},
		{"auto identical", auto(2, 5), auto(2, 5), false},
		{"both nil", nil, nil, false},
		{"nil to set", nil, fixed(3), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := defaultPoolChanged(tc.state, tc.plan); got != tc.wantChanged {
				t.Errorf("defaultPoolChanged() = %v, want %v", got, tc.wantChanged)
			}
		})
	}
}

func TestPoolMatchesDesired(t *testing.T) {
	fixed := func(n int64) *K8sDefaultPoolModel {
		return &K8sDefaultPoolModel{NodeCount: types.Int64Value(n)}
	}
	auto := func(min, max int64) *K8sDefaultPoolModel {
		return &K8sDefaultPoolModel{
			NodeCount:   types.Int64Null(),
			Autoscaling: &K8sAutoscalingModel{MinNodes: types.Int64Value(min), MaxNodes: types.Int64Value(max)},
		}
	}
	cases := []struct {
		name  string
		pool  *client.NodePool
		want  *K8sDefaultPoolModel
		match bool
	}{
		{"fixed converged", &client.NodePool{NodeCount: 3}, fixed(3), true},
		{"fixed stale count", &client.NodePool{NodeCount: 2}, fixed(3), false},
		{"fixed but still autoscaling", &client.NodePool{NodeCount: 3, AutoscaleEnabled: true}, fixed(3), false},
		{"auto converged", &client.NodePool{AutoscaleEnabled: true, MinNodes: 2, MaxNodes: 5}, auto(2, 5), true},
		{"auto not enabled yet", &client.NodePool{AutoscaleEnabled: false, MinNodes: 2, MaxNodes: 5}, auto(2, 5), false},
		{"auto stale bounds", &client.NodePool{AutoscaleEnabled: true, MinNodes: 2, MaxNodes: 8}, auto(2, 5), false},
		{"nil want", &client.NodePool{}, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := poolMatchesDesired(tc.pool, tc.want); got != tc.match {
				t.Errorf("poolMatchesDesired() = %v, want %v", got, tc.match)
			}
		})
	}
}

func TestValueOrDefault(t *testing.T) {
	cases := []struct {
		name     string
		in       types.String
		fallback string
		want     string
	}{
		{"null uses fallback", types.StringNull(), "def", "def"},
		{"empty uses fallback", types.StringValue(""), "def", "def"},
		{"unknown uses fallback", types.StringUnknown(), "def", "def"},
		{"value wins", types.StringValue("UZ-5"), "def", "UZ-5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := valueOrDefault(tc.in, tc.fallback); got != tc.want {
				t.Errorf("valueOrDefault() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestK8sNameRegex(t *testing.T) {
	valid := []string{"abc", "a1", "my-cluster", "k8s-prod-1", "a-b-c"}
	invalid := []string{"-abc", "abc-", "Abc", "ABC", "a_b", "a.b", "", "a b"}
	for _, s := range valid {
		if !k8sNameRegex.MatchString(s) {
			t.Errorf("expected %q to be valid", s)
		}
	}
	for _, s := range invalid {
		if k8sNameRegex.MatchString(s) {
			t.Errorf("expected %q to be invalid", s)
		}
	}
}

func TestClusterUpgradeConverged(t *testing.T) {
	const want = "v1.31.4"
	cases := []struct {
		name string
		cl   *client.Cluster
		ok   bool
	}{
		{"converged", &client.Cluster{Status: client.ClusterStatusSuccess, KubeVersion: want, Blocked: false}, true},
		{"stale version", &client.Cluster{Status: client.ClusterStatusSuccess, KubeVersion: "v1.30.0", Blocked: false}, false},
		{"still blocked", &client.Cluster{Status: client.ClusterStatusSuccess, KubeVersion: want, Blocked: true}, false},
		{"not success yet", &client.Cluster{Status: client.ClusterStatusProcessing, KubeVersion: want, Blocked: false}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clusterUpgradeConverged(tc.cl, want); got != tc.ok {
				t.Errorf("clusterUpgradeConverged() = %v, want %v", got, tc.ok)
			}
		})
	}
}
