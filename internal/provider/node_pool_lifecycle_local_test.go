package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"terraform-provider-prodata/internal/client"
)

// Actual Terraform, local HTTP only: no name discovery, nullable-ID checkpoint/cancellation,
// lost reserve/submit ACKs, no-op refresh and exactly one physical create/delete.
func TestNodePoolLifecycleLocalTerraformCheckpointAndRecovery(t *testing.T) {
	tf, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("local Terraform binary is not installed")
	}
	t.Setenv("TF_ACC_TERRAFORM_PATH", tf)
	t.Setenv("TF_ACC_TERRAFORM_VERSION", "")
	t.Setenv("CHECKPOINT_DISABLE", "1")
	for _, nullable := range []bool{false, true} {
		name := "lost-ack-no-op"
		if nullable {
			name = "nullable-id-positive-cancel"
		}
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			type operation struct {
				kind, state string
				pool        int64
				epoch       int64
				exists      bool
			}
			operations := map[string]*operation{}
			creators := map[int64]string{}
			firstUUID := ""
			readable := !nullable
			firstProjection := true
			createReservations, cancelled, deletionReservations, physicalCreates, physicalDeletes, legacy := 0, 0, 0, 0, 0, 0
			lostReserve, lostSubmit := !nullable, !nullable
			answer := func(w http.ResponseWriter, v any) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": v})
			}
			lostAck := func(w http.ResponseWriter) {
				hijacker, ok := w.(http.Hijacker)
				if !ok {
					t.Error("local fixture cannot emulate an acknowledgement loss")
					w.WriteHeader(500)
					return
				}
				connection, _, e := hijacker.Hijack()
				if e == nil {
					_ = connection.Close()
				}
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if q.Header.Get("X-Region") != "TEST" || q.Header.Get("X-Project-Tag") != "test" {
					t.Errorf("scope changed for %s %s", q.Method, q.URL.Path)
				}
				prefix := "/panel-main/api/v2/kubernetes/clusters/313/node-pool-operations/"
				if strings.HasPrefix(q.URL.Path, prefix) {
					suffix := strings.TrimPrefix(q.URL.Path, prefix)
					if suffix == "capabilities" {
						answer(w, map[string]any{"contract": client.NodePoolLifecycleContract, "callerUuid": true, "reserveBeforeSubmit": true, "recoverableSubmission": true, "uidPinnedDelete": true})
						return
					}
					key := strings.TrimSuffix(strings.TrimSuffix(suffix, "/reservations"), "/submit")
					view := func() {
						op := operations[key]
						if op == nil {
							w.WriteHeader(404)
							return
						}
						ready := op.state == "SUCCESS" || op.state == "NOT_SUBMITTED"
						answer(w, map[string]any{"contract": client.NodePoolLifecycleContract, "requestUuid": key, "clusterId": 313, "poolId": op.pool, "operation": op.kind, "status": op.state, "exists": op.exists, "canRelease": op.state == "NOT_SUBMITTED", "ready": ready, "nodePoolName": "workers", "epoch": op.epoch, "fingerprint": strings.Repeat("a", 64)})
					}
					if q.Method == http.MethodPost && strings.HasSuffix(suffix, "/reservations") {
						var body client.NodePoolOperationRequest
						if e := json.NewDecoder(q.Body).Decode(&body); e != nil {
							t.Error(e)
							w.WriteHeader(400)
							return
						}
						if operations[key] == nil {
							if body.Operation == "CREATE" {
								for _, old := range operations {
									if old.kind == "CREATE" && old.exists {
										t.Error("fresh UUID allocated while prior canonical creation remained owned")
										w.WriteHeader(409)
										return
									}
								}
								if body.Create == nil || body.Create.WorkerRAM != 4 || body.Create.ClusterID != 313 || body.Create.NodePoolName != "workers" {
									t.Errorf("wrong public create payload: %+v", body)
								}
								createReservations++
								id := int64(6 + createReservations)
								operations[key] = &operation{kind: "CREATE", state: "RESERVED", pool: id, epoch: int64(len(operations) + 1), exists: true}
								creators[id] = key
								if firstUUID == "" {
									firstUUID = key
								}
							} else {
								if body.PoolID == nil || creators[*body.PoolID] == "" {
									t.Error("deletion has no exact canonical pool")
									w.WriteHeader(409)
									return
								}
								deletionReservations++
								operations[key] = &operation{kind: "DELETE", state: "RESERVED", pool: *body.PoolID, epoch: int64(len(operations) + 1), exists: true}
							}
						}
						if operations[key].kind == "CREATE" && !readable {
							w.WriteHeader(503)
							return
						}
						if lostReserve && operations[key].kind == "CREATE" {
							lostReserve = false
							lostAck(w)
							return
						}
						view()
						return
					}
					if q.Method == http.MethodGet {
						if !readable {
							w.WriteHeader(503)
							return
						}
						view()
						return
					}
					if q.Method == http.MethodDelete {
						op := operations[key]
						if op == nil {
							t.Error("cancel lost original request UUID")
							w.WriteHeader(404)
							return
						}
						if op.kind != "CREATE" {
							t.Error("unexpected cancellation of a deletion intent")
						}
						if op.state == "RESERVED" {
							op.state = "NOT_SUBMITTED"
							op.exists = false
							cancelled++
						}
						view()
						return
					}
					if q.Method == http.MethodPost && strings.HasSuffix(suffix, "/submit") {
						op := operations[key]
						if op == nil {
							t.Error("submission before canonical reservation")
							w.WriteHeader(404)
							return
						}
						if op.state == "NOT_SUBMITTED" {
							t.Error("cancelled UUID was reactivated")
							w.WriteHeader(409)
							return
						}
						if op.state != "SUCCESS" {
							if op.kind == "CREATE" {
								physicalCreates++
								op.state = "SUCCESS"
							} else {
								physicalDeletes++
								op.state = "SUCCESS"
								op.exists = false
								operations[creators[op.pool]].exists = false
							}
						}
						if lostSubmit && op.kind == "CREATE" {
							lostSubmit = false
							lostAck(w)
							return
						}
						view()
						return
					}
					t.Errorf("unexpected operation %s %s", q.Method, q.URL.Path)
					w.WriteHeader(500)
					return
				}
				switch {
				case q.URL.Path == "/panel-main/api/kubernetes/getClusters":
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": []any{map[string]any{"id": 313}}})
				case q.URL.Path == "/panel-main/api/kubernetes/getCluster/313":
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": map[string]any{"id": 313, "blocked": false, "status": map[string]any{"name": "SUCCESS"}}})
				case strings.HasPrefix(q.URL.Path, "/panel-main/api/kubernetes/getK8SNodePool/"):
					id, _ := strconv.ParseInt(strings.TrimPrefix(q.URL.Path, "/panel-main/api/kubernetes/getK8SNodePool/"), 10, 64)
					if creators[id] == "" || !operations[creators[id]].exists {
						w.WriteHeader(404)
						return
					}
					if firstProjection && !nullable {
						firstProjection = false
						w.WriteHeader(503)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": map[string]any{"id": id, "clusterId": 313, "poolName": "workers", "cpu": 2, "ram": 4, "ssd": 20, "nodeCount": 1, "nodeSubnet": 24, "creationRequestUuid": creators[id], "status": map[string]any{"name": "SUCCESS"}}})
				case strings.HasPrefix(q.URL.Path, "/panel-main/api/v2/kubernetes/node-pools/") && strings.HasSuffix(q.URL.Path, "/hardware"):
					answer(w, map[string]any{"protocolVersion": 1, "vcpu": 2, "ram": 4, "diskSize": 20, "ready": true})
				default:
					legacy++
					t.Errorf("unsafe legacy/name-discovery request %s %s", q.Method, q.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer srv.Close()
			config := fmt.Sprintf(`provider "prodata" {
 api_base_url=%q
 api_key_id="fixture"
 api_secret_key="fixture"
 region="TEST"
 project_tag="test"
}
resource "prodata_kubernetes_node_pool" "workers" {
 cluster_id=313
 name="workers"
 vcpu=2
 ram=4
 disk_size=20
 node_count=1
}
`, srv.URL)
			steps := []resource.TestStep{{Config: config, ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}}}
			if nullable {
				steps = []resource.TestStep{{Config: config, ExpectError: regexp.MustCompile("Node pool reservation unverified")}, {Config: config, PreConfig: func() { mu.Lock(); readable = true; mu.Unlock() }, ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}}}
			}
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories, Steps: steps})
			mu.Lock()
			defer mu.Unlock()
			if physicalCreates != 1 || physicalDeletes != 1 || deletionReservations != 1 || legacy != 0 || firstUUID == "" {
				t.Fatalf("unstable pool identity: create/delete=%d/%d reservations=%d/%d legacy=%d", physicalCreates, physicalDeletes, createReservations, deletionReservations, legacy)
			}
			if nullable && (createReservations != 2 || cancelled != 1 || operations[firstUUID].state != "NOT_SUBMITTED") {
				t.Fatalf("nullable ID checkpoint not retained: first=%q creates=%d cancelled=%d", firstUUID, createReservations, cancelled)
			}
			if !nullable && (createReservations != 1 || cancelled != 0) {
				t.Fatalf("lost ACK caused recreation: creates=%d cancelled=%d", createReservations, cancelled)
			}
		})
	}
}
