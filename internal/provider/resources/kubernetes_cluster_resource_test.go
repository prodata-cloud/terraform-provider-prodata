package resources

import (
	"strings"
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

func TestClassifyDeletePoll(t *testing.T) {
	cases := []struct {
		status string
		want   deletePollOutcome
	}{
		{client.ClusterStatusDeleted, deletePollDone},
		{client.ClusterStatusFail, deletePollFailed},
		{client.ClusterStatusDeleting, deletePollPending},
		{client.ClusterStatusProcessing, deletePollPending},
		{client.ClusterStatusNew, deletePollPending},
		{client.ClusterStatusSuccess, deletePollPending},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			if got := classifyDeletePoll(tc.status); got != tc.want {
				t.Errorf("classifyDeletePoll(%q) = %d, want %d", tc.status, got, tc.want)
			}
		})
	}
}

// drivePollSequence replays the Delete poll loop's decision over a sequence of
// observed statuses: it keeps polling while pending and stops on the first
// done/failed outcome, mirroring the loop in Delete.
func drivePollSequence(statuses []string) deletePollOutcome {
	outcome := deletePollPending
	for _, s := range statuses {
		outcome = classifyDeletePoll(s)
		if outcome != deletePollPending {
			break
		}
	}
	return outcome
}

func TestClassifyDeletePollSequence(t *testing.T) {
	// A teardown that lingers in DELETING and then reads DELETED must succeed.
	if got := drivePollSequence([]string{
		client.ClusterStatusProcessing,
		client.ClusterStatusDeleting,
		client.ClusterStatusDeleting,
		client.ClusterStatusDeleted,
	}); got != deletePollDone {
		t.Errorf("DELETING...->DELETED sequence = %d, want deletePollDone (%d)", got, deletePollDone)
	}
	// A teardown that ends in FAIL must report failure (Delete leaves it in state).
	if got := drivePollSequence([]string{
		client.ClusterStatusDeleting,
		client.ClusterStatusFail,
	}); got != deletePollFailed {
		t.Errorf("DELETING->FAIL sequence = %d, want deletePollFailed (%d)", got, deletePollFailed)
	}
}

func TestAdoptConflictDiag(t *testing.T) {
	cases := []struct {
		name         string
		cluster      *client.Cluster
		wantDetail   string // substring the detail must contain
		wantNoImport bool   // the detail must NOT suggest terraform import
	}{
		{
			name:         "deleting cluster still holds the name",
			cluster:      &client.Cluster{ID: 7, Name: "web", Status: client.ClusterStatusDeleting},
			wantDetail:   "still being deleted",
			wantNoImport: true,
		},
		{
			name:         "failed cluster must be deleted first",
			cluster:      &client.Cluster{ID: 8, Name: "web", Status: client.ClusterStatusFail},
			wantDetail:   "delete it before recreating",
			wantNoImport: true,
		},
		{
			name:         "live collision is adoptable via import",
			cluster:      &client.Cluster{ID: 9, Name: "web", Status: client.ClusterStatusSuccess},
			wantDetail:   "already exists (id 9)",
			wantNoImport: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			summary, detail := adoptConflictDiag(tc.cluster)
			if summary == "" {
				t.Error("expected a non-empty summary")
			}
			if !strings.Contains(detail, tc.wantDetail) {
				t.Errorf("detail = %q, want it to contain %q", detail, tc.wantDetail)
			}
			mentionsImport := strings.Contains(detail, "import")
			if tc.wantNoImport && mentionsImport {
				t.Errorf("detail for status %s should not suggest import: %q", tc.cluster.Status, detail)
			}
			if !tc.wantNoImport && !mentionsImport {
				t.Errorf("detail for a live collision should suggest import: %q", detail)
			}
		})
	}
}
