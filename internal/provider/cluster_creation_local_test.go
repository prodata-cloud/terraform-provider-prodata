package provider

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"terraform-provider-prodata/internal/client"
)

// Actual Terraform verifies the pre-HTTP nullable-ID checkpoint, taint cleanup, lost ACK recovery,
// clean refresh plan and exactly one physical cluster creation/deletion. Local HTTP only.
func TestClusterCreationLocalTerraformCheckpointAndRecovery(t *testing.T) {
	tf, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("local Terraform binary unavailable")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", tf)
	t.Setenv("TF_ACC_TERRAFORM_VERSION", "")
	t.Setenv("CHECKPOINT_DISABLE", "1")
	for _, nullable := range []bool{false, true} {
		t.Run(map[bool]string{false: "lost-ACK-no-op", true: "nullable-ID-cancellation"}[nullable], func(t *testing.T) {
			var mu sync.Mutex
			type entry struct {
				id     int64
				status string
				exists bool
				epoch  int64
			}
			entries := map[string]*entry{}
			owner := ""
			readable := !nullable
			reserves, cancels, physical, deletes, legacy := 0, 0, 0, 0, 0
			lostReserve, lostSubmit := !nullable, !nullable
			answer := func(w http.ResponseWriter, data any) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
			}
			v1 := func(w http.ResponseWriter, data any) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": data})
			}
			lost := func(w http.ResponseWriter) {
				h, ok := w.(http.Hijacker)
				if !ok {
					t.Error("fixture cannot lose ACK")
					w.WriteHeader(500)
					return
				}
				c, _, e := h.Hijack()
				if e == nil {
					_ = c.Close()
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if q.Header.Get("X-Region") != "TEST" || q.Header.Get("X-Project-Tag") != "test" {
					t.Error("cluster operation lost scope")
				}
				prefix := "/panel-main/api/v2/kubernetes/cluster-operations/"
				if strings.HasPrefix(q.URL.Path, prefix) {
					tail := strings.TrimPrefix(q.URL.Path, prefix)
					if tail == "capabilities" {
						answer(w, map[string]any{"contract": client.ClusterCreationContract, "callerUuid": true, "reserveBeforeSubmit": true, "recoverableSubmission": true, "resolveTombstone": true, "compensatingDeleteFence": true, "journalIdentityFence": true})
						return
					}
					key := strings.TrimSuffix(strings.TrimSuffix(tail, "/reservations"), "/submit")
					view := func() {
						e := entries[key]
						if e == nil {
							w.WriteHeader(404)
							return
						}
						var id any = e.id
						if e.id == 0 {
							id = nil
						}
						answer(w, map[string]any{"contract": client.ClusterCreationContract, "requestUuid": key, "clusterId": id, "status": e.status, "exists": e.exists, "canRelease": e.status == "NOT_SUBMITTED", "ready": e.status == "SUCCESS" || e.status == "NOT_SUBMITTED", "epoch": e.epoch, "fingerprint": strings.Repeat("a", 64), "name": "tfc"})
					}
					if q.Method == "POST" && strings.HasSuffix(tail, "/reservations") {
						var req client.CreateClusterRequest
						if e := json.NewDecoder(q.Body).Decode(&req); e != nil {
							t.Error(e)
						}
						if req.ClusterName != "tfc" || req.MasterNodeConfigID != 7 || req.LocalNetID != 7 {
							t.Errorf("incorrect immutable body %+v", req)
						}
						if entries[key] == nil {
							for _, e := range entries {
								if e.exists {
									t.Error("new UUID before original ownership released")
									w.WriteHeader(409)
									return
								}
							}
							reserves++
							entries[key] = &entry{id: int64(312 + reserves), status: "RESERVED", exists: true, epoch: int64(reserves)}
						}
						if !readable {
							w.WriteHeader(503)
							return
						}
						if lostReserve {
							lostReserve = false
							lost(w)
							return
						}
						view()
						return
					}
					if q.Method == "GET" {
						if !readable {
							w.WriteHeader(503)
							return
						}
						view()
						return
					}
					if q.Method == "DELETE" {
						e := entries[key]
						if e == nil {
							entries[key] = &entry{status: "NOT_SUBMITTED", epoch: int64(len(entries) + 1)}
							cancels++
						} else if e.status == "RESERVED" {
							e.status = "NOT_SUBMITTED"
							e.exists = false
							cancels++
						}
						view()
						return
					}
					if q.Method == "POST" && strings.HasSuffix(tail, "/submit") {
						e := entries[key]
						if e == nil || e.status == "NOT_SUBMITTED" {
							t.Error("unowned/cancelled request activated")
							w.WriteHeader(409)
							return
						}
						if e.status != "SUCCESS" {
							physical++
							e.status = "SUCCESS"
							owner = key
						}
						if lostSubmit {
							lostSubmit = false
							lost(w)
							return
						}
						view()
						return
					}
					t.Errorf("unexpected lifecycle request %s %s", q.Method, q.URL.Path)
					w.WriteHeader(500)
					return
				}
				switch {
				case q.URL.Path == "/panel-main/api/kubernetes/getMasterNodeConfig/false":
					v1(w, []map[string]any{{"id": 7, "cpu": 2, "ram": 4, "ssd": 20, "isHa": false}})
				case q.URL.Path == "/panel-main/api/kubernetes/getClusters":
					v1(w, []any{})
				case strings.HasPrefix(q.URL.Path, "/panel-main/api/kubernetes/getCluster/"):
					if owner == "" {
						w.WriteHeader(404)
						return
					}
					e := entries[owner]
					status := "SUCCESS"
					if !e.exists {
						status = "DELETED"
					}
					v1(w, map[string]any{"id": e.id, "name": "tfc", "kuberVersion": "v1.31.4", "status": map[string]any{"name": status}, "isHa": false, "isPublic": false, "podSubnet": "10.244.0.0/16", "nodeIpRange": "10.0.0.10-10.0.0.20", "masterNodeConfiguration": map[string]any{"id": 7, "cpu": 2, "ram": 4, "ssd": 20}, "blocked": false, "masterNodeCount": 1, "workerNodeCount": 0, "nodePoolCount": 1, "creationRequestUuid": owner, "clusterConfigSecret": base64.StdEncoding.EncodeToString([]byte("apiVersion: v1\n")), "deletionContract": "cluster-deletion-v1", "deletionVerified": !e.exists, "deletionOperationUuid": "11111111-1111-4111-8111-111111111111"})
				case strings.HasPrefix(q.URL.Path, "/panel-main/api/kubernetes/deleteCluster/"):
					if owner == "" {
						t.Error("delete without original creator")
						w.WriteHeader(404)
						return
					}
					if entries[owner].exists {
						entries[owner].exists = false
						deletes++
					}
					v1(w, nil)
				case strings.HasSuffix(q.URL.Path, "/deletion/capabilities"):
					answer(w, map[string]any{"contract": "cluster-deletion-v1", "canonicalIdResume": true, "reserveBeforeSubmit": true, "recoverableSubmission": true, "uidPinnedDelete": true})
				case strings.HasSuffix(q.URL.Path, "/hardware"):
					answer(w, map[string]any{"protocolVersion": 1, "masterFlavorId": 7, "ready": true})
				default:
					legacy++
					t.Errorf("unexpected legacy mutation %s %s", q.Method, q.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			config := fmt.Sprintf(`provider "prodata" {
 api_base_url=%q
 api_key_id="fixture"
 api_secret_key="fixture"
 region="TEST"
 project_tag="test"
}
resource "prodata_kubernetes_cluster" "test" {
 name="tfc"
 kubernetes_version="v1.31.4"
 network_id=7
 pod_cidr="10.244.0.0/16"
 master_flavor_id=7
 exclude_credentials_from_state=true
}
`, server.URL)
			checks := resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
			steps := []resource.TestStep{{Config: config, ConfigPlanChecks: checks}}
			if nullable {
				steps = []resource.TestStep{{Config: config, ExpectError: regexp.MustCompile("Cluster Reservation Is Unverified")}, {Config: config, PreConfig: func() { mu.Lock(); readable = true; mu.Unlock() }, ConfigPlanChecks: checks}}
			}
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories, Steps: steps})
			mu.Lock()
			defer mu.Unlock()
			if physical != 1 || deletes != 1 || legacy != 0 || reserves != 1+map[bool]int{false: 0, true: 1}[nullable] || cancels != map[bool]int{false: 0, true: 1}[nullable] {
				t.Fatalf("reserve/cancel/create/delete/legacy=%d/%d/%d/%d/%d", reserves, cancels, physical, deletes, legacy)
			}
		})
	}
}
