package resources

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func clusterDeletionState(t *testing.T) tfsdk.State {
	t.Helper()
	_, sch := k8sObjType(t)
	return tfsdk.State{Schema: sch, Raw: k8sRaw(t, map[string]tftypes.Value{"id": nirNum(313), "name": nirStr("cluster"), "region": nirStr("TEST"), "project_tag": nirStr("selected"), "status": nirStr("SUCCESS")})}
}

func TestClusterMissingAndUnverifiedDeletionNeverEraseOwnership(t *testing.T) {
	for _, mode := range []string{"raw404", "legacyMissing", "legacyDeleted", "wrongProof", "malformedUuid", "wrongCanonicalId"} {
		t.Run(mode, func(t *testing.T) {
			c := protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
				if mode == "raw404" {
					http.NotFound(w, q)
					return
				}
				if mode == "legacyMissing" {
					w.WriteHeader(500)
					_, _ = w.Write([]byte(`{"error":703,"errMessage":"Cluster not found!"}`))
					return
				}
				data := map[string]any{"id": 313, "name": "cluster", "status": map[string]any{"name": "DELETED"}}
				if mode != "legacyDeleted" {
					data["deletionContract"], data["deletionVerified"], data["deletionOperationUuid"] = "cluster-deletion-v1", true, "11111111-1111-4111-8111-111111111111"
				}
				if mode == "wrongProof" {
					data["deletionContract"] = "other-contract"
				}
				if mode == "malformedUuid" {
					data["deletionOperationUuid"] = "not-a-uuid"
				}
				if mode == "wrongCanonicalId" {
					data["id"] = 99
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": data})
			})
			r := &K8sClusterResource{c: c}
			state := clusterDeletionState(t)
			read := resource.ReadResponse{State: state}
			r.Read(context.Background(), resource.ReadRequest{State: state}, &read)
			if read.State.Raw.IsNull() || len(read.Diagnostics) == 0 {
				t.Fatalf("unproven absence erased cluster: mode=%s diagnostics=%v", mode, read.Diagnostics)
			}
			deleted := resource.DeleteResponse{State: state}
			r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleted)
			if !deleted.Diagnostics.HasError() {
				t.Fatalf("destroy accepted unproven deletion: mode=%s diagnostics=%v", mode, deleted.Diagnostics)
			}
		})
	}
}

func TestClusterVerifiedCanonicalDeletionReleasesOwnership(t *testing.T) {
	c := protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": map[string]any{"id": 313, "name": "cluster", "status": map[string]any{"name": "DELETED"}, "deletionContract": "cluster-deletion-v1", "deletionVerified": true, "deletionOperationUuid": "11111111-1111-4111-8111-111111111111"}})
	})
	r := &K8sClusterResource{c: c}
	state := clusterDeletionState(t)
	read := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &read)
	if read.Diagnostics.HasError() || !read.State.Raw.IsNull() {
		t.Fatalf("verified deletion did not release: %v", read.Diagnostics)
	}
	deleted := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleted)
	if deleted.Diagnostics.HasError() {
		t.Fatal(deleted.Diagnostics)
	}
}

func TestClusterMissingPollAfterDeleteAckRetainsOwnership(t *testing.T) {
	reads, posts := 0, 0
	c := protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
		if strings.HasSuffix(q.URL.Path, "/deletion/capabilities") {
			protectionReply(w, map[string]any{"contract": "cluster-deletion-v1", "canonicalIdResume": true, "reserveBeforeSubmit": true, "recoverableSubmission": true, "uidPinnedDelete": true})
			return
		}
		if q.Method == http.MethodGet {
			reads++
			if reads == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": map[string]any{"id": 313, "name": "cluster", "status": map[string]any{"name": "SUCCESS"}}})
				return
			}
			http.NotFound(w, q)
			return
		}
		posts++
		_ = json.NewEncoder(w).Encode(map[string]any{"error": 0})
	})
	r := &K8sClusterResource{c: c}
	state := clusterDeletionState(t)
	out := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &out)
	if !out.Diagnostics.HasError() || posts != 1 || reads != 2 {
		t.Fatalf("postACK unknown released: posts=%d reads=%d diag=%v", posts, reads, out.Diagnostics)
	}
}

func TestClusterKnownDeletionDrainsWithoutNewCapabilityOrPost(t *testing.T) {
	reads := 0
	c := protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
		if q.Method != "GET" || !strings.HasSuffix(q.URL.Path, "/getCluster/313") {
			t.Errorf("known drain tried new admission/writer: %s %s", q.Method, q.URL.Path)
			w.WriteHeader(503)
			return
		}
		reads++
		status := "DELETING"
		if reads > 1 {
			status = "DELETED"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": map[string]any{"id": 313, "name": "cluster", "status": map[string]any{"name": status}, "deletionContract": "cluster-deletion-v1", "deletionVerified": reads > 1, "deletionOperationUuid": guestUUID}})
	})
	r := &K8sClusterResource{c: c}
	state := clusterDeletionState(t)
	out := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &out)
	if out.Diagnostics.HasError() || reads != 2 {
		t.Fatalf("known deletion was gated: reads=%d diag=%v", reads, out.Diagnostics)
	}
}

func TestClusterMissingDeletionCapabilityRefusesBeforeLegacyMutation(t *testing.T) {
	posts := 0
	c := protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
		if q.Method != "GET" {
			posts++
			t.Error("legacy deletion before durable capability")
		}
		if strings.HasSuffix(q.URL.Path, "/deletion/capabilities") {
			protectionReply(w, map[string]any{"contract": "cluster-deletion-v1", "canonicalIdResume": true, "reserveBeforeSubmit": true, "recoverableSubmission": true})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": map[string]any{"id": 313, "name": "cluster", "status": map[string]any{"name": "SUCCESS"}}})
	})
	r := &K8sClusterResource{c: c}
	state := clusterDeletionState(t)
	out := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &out)
	if !out.Diagnostics.HasError() || posts != 0 {
		t.Fatalf("mixed backend allowed to delete: posts=%d diag=%v", posts, out.Diagnostics)
	}
}
