package resources

import (
	"testing"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestParseK8sPoolImportID(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		wantCluster int64
		wantPool    int64
		wantRegion  string
		wantProject string
		wantErr     bool
	}{
		{"bare", "42/7", 42, 7, "", "", false},
		{"composite", "UZ-5/42/7@my-project", 42, 7, "UZ-5", "my-project", false},
		{"composite hyphenated project", "KZ-1/42/7@team-prod-1", 42, 7, "KZ-1", "team-prod-1", false},
		{"bare single id", "42", 0, 0, "", "", true},
		{"bare three segments", "42/7/9", 0, 0, "", "", true},
		{"bare non-int cluster", "x/7", 0, 0, "", "", true},
		{"bare non-int pool", "42/x", 0, 0, "", "", true},
		{"composite missing project", "UZ-5/42/7@", 0, 0, "", "", true},
		{"composite missing region", "/42/7@p", 0, 0, "", "", true},
		{"composite too few segments", "UZ-5/7@p", 0, 0, "", "", true},
		{"composite non-int pool", "UZ-5/42/x@p", 0, 0, "", "", true},
		{"composite non-int cluster", "UZ-5/x/7@p", 0, 0, "", "", true},
		{"empty", "", 0, 0, "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clusterID, poolID, region, project, err := parseK8sPoolImportID(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got cluster=%d pool=%d region=%q project=%q",
						tc.in, clusterID, poolID, region, project)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if clusterID != tc.wantCluster || poolID != tc.wantPool || region != tc.wantRegion || project != tc.wantProject {
				t.Errorf("got (%d,%d,%q,%q), want (%d,%d,%q,%q)",
					clusterID, poolID, region, project, tc.wantCluster, tc.wantPool, tc.wantRegion, tc.wantProject)
			}
		})
	}
}

func TestNodePoolChanged(t *testing.T) {
	fixed := func(n int64) *K8sNodePoolModel {
		return &K8sNodePoolModel{NodeCount: types.Int64Value(n)}
	}
	auto := func(min, max int64) *K8sNodePoolModel {
		return &K8sNodePoolModel{
			NodeCount:   types.Int64Null(),
			Autoscaling: &K8sAutoscalingModel{MinNodes: types.Int64Value(min), MaxNodes: types.Int64Value(max)},
		}
	}

	cases := []struct {
		name        string
		state, plan *K8sNodePoolModel
		wantChanged bool
	}{
		{"identical fixed", fixed(3), fixed(3), false},
		{"node_count changed", fixed(3), fixed(5), true},
		{"fixed to auto", fixed(3), auto(2, 5), true},
		{"auto to fixed", auto(2, 5), fixed(3), true},
		{"auto bounds changed", auto(2, 5), auto(2, 8), true},
		{"auto identical", auto(2, 5), auto(2, 5), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nodePoolChanged(tc.state, tc.plan); got != tc.wantChanged {
				t.Errorf("nodePoolChanged() = %v, want %v", got, tc.wantChanged)
			}
		})
	}
}

func TestNodePoolMatchesDesired(t *testing.T) {
	fixed := func(n int64) *K8sNodePoolModel {
		return &K8sNodePoolModel{NodeCount: types.Int64Value(n)}
	}
	auto := func(min, max int64) *K8sNodePoolModel {
		return &K8sNodePoolModel{
			NodeCount:   types.Int64Null(),
			Autoscaling: &K8sAutoscalingModel{MinNodes: types.Int64Value(min), MaxNodes: types.Int64Value(max)},
		}
	}
	cases := []struct {
		name  string
		pool  *client.NodePool
		want  *K8sNodePoolModel
		match bool
	}{
		{"fixed converged", &client.NodePool{NodeCount: 3}, fixed(3), true},
		{"fixed stale count", &client.NodePool{NodeCount: 2}, fixed(3), false},
		{"fixed but still autoscaling", &client.NodePool{NodeCount: 3, AutoscaleEnabled: true}, fixed(3), false},
		{"fixed count unknown", &client.NodePool{NodeCount: 9}, &K8sNodePoolModel{NodeCount: types.Int64Unknown()}, true},
		{"auto converged", &client.NodePool{AutoscaleEnabled: true, MinNodes: 2, MaxNodes: 5}, auto(2, 5), true},
		{"auto not enabled yet", &client.NodePool{AutoscaleEnabled: false, MinNodes: 2, MaxNodes: 5}, auto(2, 5), false},
		{"auto stale bounds", &client.NodePool{AutoscaleEnabled: true, MinNodes: 2, MaxNodes: 8}, auto(2, 5), false},
		{"nil want", &client.NodePool{}, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nodePoolMatchesDesired(tc.pool, tc.want); got != tc.match {
				t.Errorf("nodePoolMatchesDesired() = %v, want %v", got, tc.match)
			}
		})
	}
}

func TestApplyServerState(t *testing.T) {
	r := &K8sNodePoolResource{}
	// A fixed-size pool with sizing that differs from any prior model, to prove the
	// immutable-input preservation rules.
	pool := &client.NodePool{
		ID: 7, ClusterID: 42, Name: "workers", NodeCount: 5, NodeSubnet: 26,
		CPU: 4, RAM: 8, SSD: 100, Status: "SUCCESS", AutoscaleEnabled: false,
	}

	t.Run("fromRead import populates null immutable inputs", func(t *testing.T) {
		m := &K8sNodePoolModel{
			ClusterID: types.Int64Null(), Name: types.StringNull(),
			VCPU: types.Int64Null(), RAM: types.Int64Null(), DiskSize: types.Int64Null(),
		}
		r.applyServerState(m, pool, "UZ-5", "proj", true)
		if m.ClusterID.ValueInt64() != 42 || m.Name.ValueString() != "workers" ||
			m.VCPU.ValueInt64() != 4 || m.RAM.ValueInt64() != 8 || m.DiskSize.ValueInt64() != 100 {
			t.Errorf("import did not populate immutable inputs from server: %+v", m)
		}
		if m.ID.ValueInt64() != 7 || m.NodeCount.ValueInt64() != 5 || m.NodeSubnet.ValueInt64() != 26 ||
			m.Status.ValueString() != "SUCCESS" || m.Region.ValueString() != "UZ-5" || m.ProjectTag.ValueString() != "proj" {
			t.Errorf("computed/scope fields not set: %+v", m)
		}
		if m.Autoscaling != nil {
			t.Errorf("autoscaling should be nil for a fixed pool")
		}
	})

	t.Run("fromRead preserves already-set immutable inputs (RAM unit guard)", func(t *testing.T) {
		m := &K8sNodePoolModel{
			ClusterID: types.Int64Value(42), Name: types.StringValue("workers"),
			VCPU: types.Int64Value(4), RAM: types.Int64Value(8), DiskSize: types.Int64Value(100),
		}
		// Server echoes ram in a different unit; preservation must keep the state GB value.
		echoed := *pool
		echoed.RAM = 8192
		r.applyServerState(m, &echoed, "UZ-5", "proj", true)
		if m.RAM.ValueInt64() != 8 {
			t.Errorf("RAM should be preserved from state (8), got %d", m.RAM.ValueInt64())
		}
	})

	t.Run("fromRead=false leaves immutable inputs untouched", func(t *testing.T) {
		m := &K8sNodePoolModel{
			ClusterID: types.Int64Value(42), Name: types.StringValue("workers"),
			VCPU: types.Int64Value(4), RAM: types.Int64Value(8), DiskSize: types.Int64Value(100),
		}
		echoed := *pool
		echoed.CPU, echoed.RAM, echoed.SSD = 99, 99, 99
		r.applyServerState(m, &echoed, "UZ-5", "proj", false)
		if m.VCPU.ValueInt64() != 4 || m.RAM.ValueInt64() != 8 || m.DiskSize.ValueInt64() != 100 {
			t.Errorf("Create/Update must not overwrite immutable inputs from server: %+v", m)
		}
	})

	t.Run("autoscaling pool reconstructs the block from server", func(t *testing.T) {
		auto := &client.NodePool{
			ID: 7, ClusterID: 42, Name: "workers", NodeCount: 3, Status: "SUCCESS",
			AutoscaleEnabled: true, MinNodes: 2, MaxNodes: 6,
		}
		m := &K8sNodePoolModel{Autoscaling: nil}
		r.applyServerState(m, auto, "UZ-5", "proj", false)
		if m.Autoscaling == nil ||
			m.Autoscaling.MinNodes.ValueInt64() != 2 || m.Autoscaling.MaxNodes.ValueInt64() != 6 {
			t.Errorf("autoscaling block not reconstructed: %+v", m.Autoscaling)
		}
		if m.NodeCount.ValueInt64() != 3 {
			t.Errorf("node_count should track the server value, got %d", m.NodeCount.ValueInt64())
		}
	})

	t.Run("disabling autoscaling clears the block", func(t *testing.T) {
		m := &K8sNodePoolModel{
			Autoscaling: &K8sAutoscalingModel{MinNodes: types.Int64Value(2), MaxNodes: types.Int64Value(6)},
		}
		r.applyServerState(m, pool, "UZ-5", "proj", false)
		if m.Autoscaling != nil {
			t.Errorf("autoscaling block should be cleared when the server reports it off")
		}
	})
}
