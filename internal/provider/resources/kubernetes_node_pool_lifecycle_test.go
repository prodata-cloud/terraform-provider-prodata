package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"terraform-provider-prodata/internal/client"
)

func poolLifecycleView(uuid, status, operation string, poolID any) map[string]any {
	exists, release, ready := true, false, false
	if status == "SUCCESS" {
		ready = true
		exists = operation == "CREATE"
	}
	if status == "NOT_SUBMITTED" {
		exists = operation == "DELETE"
		release = true
		ready = true
	}
	return map[string]any{"contract": client.NodePoolLifecycleContract, "requestUuid": uuid, "clusterId": 313, "poolId": poolID, "operation": operation, "status": status, "exists": exists, "canRelease": release, "ready": ready, "nodePoolName": "workers", "epoch": 91, "fingerprint": strings.Repeat("a", 64)}
}
func poolLifecycleFixture(t *testing.T, url string) (*K8sNodePoolResource, resource.CreateRequest, *resource.CreateResponse) {
	t.Helper()
	c, err := client.New(client.Config{APIBaseURL: url, APIKeyID: "test", APISecretKey: "test", Region: "TEST", ProjectTag: "test"})
	if err != nil {
		t.Fatal(err)
	}
	r := &K8sNodePoolResource{c: c, lifecyclePollInterval: time.Millisecond}
	plan, state := dependentProjectPlan(t, r, "node_pool", "test")
	return r, resource.CreateRequest{Plan: plan}, &resource.CreateResponse{State: state}
}
func poolLifecycleCapabilities() map[string]any {
	return map[string]any{"contract": client.NodePoolLifecycleContract, "callerUuid": true, "reserveBeforeSubmit": true, "recoverableSubmission": true, "uidPinnedDelete": true}
}
func poolCanonicalWire() map[string]any {
	return map[string]any{"id": 7, "clusterId": 313, "poolName": "workers", "cpu": 2, "ram": 4, "ssd": 20, "nodeCount": 1, "nodeSubnet": 24, "status": map[string]any{"name": "SUCCESS"}}
}

func TestNodePoolLifecycleHistoricalDeletionNeedsNoMutableName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var data any
		if strings.HasSuffix(q.URL.Path, "/capabilities") {
			data = poolLifecycleCapabilities()
		} else {
			if q.Method != http.MethodPost || !strings.HasSuffix(q.URL.Path, "/reservations") {
				t.Errorf("historical proof caused another mutation: %s %s", q.Method, q.URL.Path)
			}
			parts := strings.Split(q.URL.Path, "/")
			view := poolLifecycleView(parts[len(parts)-2], "SUCCESS", "DELETE", 7)
			view["nodePoolName"] = nil
			data = view
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
	}))
	defer server.Close()
	r, request, _ := poolLifecycleFixture(t, server.URL)
	state := tfsdk.State(request.Plan)
	for name, value := range map[string]any{"id": int64(7), "region": "TEST", "status": "SUCCESS", "node_subnet": int64(24)} {
		if d := state.SetAttribute(context.Background(), path.Root(name), value); d.HasError() {
			t.Fatal(d)
		}
	}
	response := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &response)
	if response.Diagnostics.HasError() {
		t.Fatal(response.Diagnostics)
	}
}
func TestNodePoolLifecycleCheckpointLostAcksAndKnownPreclaimRetry(t *testing.T) {
	var output *resource.CreateResponse
	knownUUID, status := "", ""
	reserves, submits, physical, legacy := 0, 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		answer := func(v any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": v})
		}
		base := "/panel-main/api/v2/kubernetes/clusters/313/node-pool-operations/"
		if strings.HasPrefix(q.URL.Path, base) {
			suffix := strings.TrimPrefix(q.URL.Path, base)
			if suffix == "capabilities" {
				var checkpoint K8sNodePoolModel
				if d := output.State.Get(context.Background(), &checkpoint); d.HasError() {
					t.Error(d)
				}
				if checkpoint.RequestUUID.ValueString() == "" || !checkpoint.ID.IsNull() || checkpoint.Status.IsUnknown() || checkpoint.NodeSubnet.IsUnknown() {
					t.Errorf("no fully-known pre-HTTP checkpoint: %+v", checkpoint)
				}
				answer(poolLifecycleCapabilities())
				return
			}
			id := strings.TrimSuffix(strings.TrimSuffix(suffix, "/reservations"), "/submit")
			if knownUUID != "" && id != knownUUID {
				t.Error("operation UUID changed")
			}
			knownUUID = id
			if q.Method == http.MethodPost && strings.HasSuffix(suffix, "/reservations") {
				reserves++
				status = "RESERVED"
				lostVolumeAck(w)
				return
			}
			if q.Method == http.MethodPost && strings.HasSuffix(suffix, "/submit") {
				var checkpoint K8sNodePoolModel
				if d := output.State.Get(context.Background(), &checkpoint); d.HasError() {
					t.Error(d)
				}
				if checkpoint.ID.ValueInt64() != 7 || checkpoint.RequestUUID.ValueString() != id {
					t.Error("pool ID not checkpointed before submit")
				}
				submits++
				if submits == 1 {
					status = "UNVERIFIED"
					answer(poolLifecycleView(id, status, "CREATE", 7))
					return
				}
				if physical == 0 {
					physical++
				}
				status = "SUCCESS"
				lostVolumeAck(w)
				return
			}
			if q.Method == http.MethodGet {
				answer(poolLifecycleView(id, status, "CREATE", 7))
				return
			}
			t.Errorf("unexpected lifecycle %s %s", q.Method, q.URL.Path)
			http.NotFound(w, q)
			return
		}
		switch q.URL.Path {
		case "/panel-main/api/kubernetes/getClusters":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": []any{map[string]any{"id": 313}}})
		case "/panel-main/api/kubernetes/getCluster/313":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": map[string]any{"id": 313, "blocked": false, "status": map[string]any{"name": "SUCCESS"}}})
		case "/panel-main/api/kubernetes/getK8SNodePool/7":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": poolCanonicalWire()})
		default:
			legacy++
			t.Errorf("unsafe legacy/discovery call %s %s", q.Method, q.URL.Path)
			http.NotFound(w, q)
		}
	}))
	defer server.Close()
	r, request, response := poolLifecycleFixture(t, server.URL)
	output = response
	r.Create(context.Background(), request, response)
	if response.Diagnostics.HasError() {
		t.Fatal(response.Diagnostics)
	}
	var got K8sNodePoolModel
	if d := response.State.Get(context.Background(), &got); d.HasError() {
		t.Fatal(d)
	}
	if got.ID.ValueInt64() != 7 || got.RequestUUID.ValueString() != knownUUID || got.Status.ValueString() != "SUCCESS" || reserves != 1 || submits != 2 || physical != 1 || legacy != 0 {
		t.Fatalf("unsafe recovery model=%+v counts=%d/%d/%d/%d", got, reserves, submits, physical, legacy)
	}
}
func TestNodePoolLifecycleReadUnknownNeverSubmitsOrDropsOwnership(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		calls++
		if q.Method != http.MethodGet {
			t.Error("Read submitted a mutation")
		}
		w.WriteHeader(404)
	}))
	defer server.Close()
	r, request, _ := poolLifecycleFixture(t, server.URL)
	state := tfsdk.State(request.Plan)
	for name, value := range map[string]any{"id": int64(7), "region": "TEST", "request_uuid": "54f17ae4-2f21-4c02-aafd-a9c9adc7b455", "status": "SUCCESS", "node_subnet": int64(24)} {
		if d := state.SetAttribute(context.Background(), path.Root(name), value); d.HasError() {
			t.Fatal(d)
		}
	}
	response := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &response)
	if response.Diagnostics.HasError() || len(response.Diagnostics) == 0 || response.State.Raw.IsNull() || !response.State.Raw.Equal(state.Raw) || calls != 1 {
		t.Fatalf("unknown read lost ownership: %v calls=%d", response.Diagnostics, calls)
	}
}
func TestNodePoolLifecycleNullableIdCancellationDoesNotUseLegacyDelete(t *testing.T) {
	const id = "54f17ae4-2f21-4c02-aafd-a9c9adc7b455"
	cancels := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.Method != http.MethodDelete || !strings.HasSuffix(q.URL.Path, "/node-pool-operations/"+id) {
			t.Errorf("unsafe cleanup %s %s", q.Method, q.URL.Path)
			http.NotFound(w, q)
			return
		}
		cancels++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": poolLifecycleView(id, "NOT_SUBMITTED", "CREATE", nil)})
	}))
	defer server.Close()
	r, request, _ := poolLifecycleFixture(t, server.URL)
	state := tfsdk.State(request.Plan)
	for name, value := range map[string]any{"request_uuid": id, "status": "RESERVING", "node_subnet": types.Int64Null(), "id": types.Int64Null(), "region": "TEST"} {
		if d := state.SetAttribute(context.Background(), path.Root(name), value); d.HasError() {
			t.Fatal(d)
		}
	}
	response := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &response)
	if response.Diagnostics.HasError() || cancels != 1 {
		t.Fatalf("positive nullable-ID cleanup failed: %v cancels=%d", response.Diagnostics, cancels)
	}
}

func TestNodePoolLifecycleDeleteCheckpointUnknownThenSameUuidRetry(t *testing.T) {
	const createUUID = "54f17ae4-2f21-4c02-aafd-a9c9adc7b455"
	var output *resource.DeleteResponse
	deletionUUID, status := "", ""
	readable := false
	reserves, submits, physical, legacy := 0, 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		answer := func(v any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": v})
		}
		base := "/panel-main/api/v2/kubernetes/clusters/313/node-pool-operations/"
		if !strings.HasPrefix(q.URL.Path, base) {
			legacy++
			http.NotFound(w, q)
			return
		}
		suffix := strings.TrimPrefix(q.URL.Path, base)
		if suffix == "capabilities" {
			answer(poolLifecycleCapabilities())
			return
		}
		key := strings.TrimSuffix(strings.TrimSuffix(suffix, "/reservations"), "/submit")
		if key == createUUID && q.Method == http.MethodDelete {
			answer(poolLifecycleView(createUUID, "SUCCESS", "CREATE", 7))
			return
		}
		if deletionUUID != "" && key != deletionUUID {
			t.Error("unknown deletion changed UUID")
		}
		deletionUUID = key
		if strings.HasSuffix(suffix, "/reservations") {
			reserves++
			var checkpoint K8sNodePoolModel
			if d := output.State.Get(context.Background(), &checkpoint); d.HasError() {
				t.Error(d)
			}
			if checkpoint.DeletionRequestUUID.ValueString() != key || checkpoint.ID.ValueInt64() != 7 {
				t.Error("deletion UUID was not checkpointed before reserve")
			}
			status = "RESERVED"
			w.WriteHeader(503)
			return
		}
		if q.Method == http.MethodGet {
			if !readable {
				w.WriteHeader(503)
				return
			}
			answer(poolLifecycleView(key, status, "DELETE", 7))
			return
		}
		if strings.HasSuffix(suffix, "/submit") {
			submits++
			if submits == 1 {
				status = "UNVERIFIED"
				answer(poolLifecycleView(key, status, "DELETE", 7))
				return
			}
			if physical == 0 {
				physical++
			}
			status = "SUCCESS"
			answer(poolLifecycleView(key, status, "DELETE", 7))
			return
		}
		t.Errorf("unexpected %s %s", q.Method, q.URL.Path)
		http.NotFound(w, q)
	}))
	defer server.Close()
	r, request, _ := poolLifecycleFixture(t, server.URL)
	state := tfsdk.State(request.Plan)
	for name, value := range map[string]any{"id": int64(7), "request_uuid": createUUID, "region": "TEST", "status": "SUCCESS", "node_subnet": int64(24)} {
		if d := state.SetAttribute(context.Background(), path.Root(name), value); d.HasError() {
			t.Fatal(d)
		}
	}
	first := resource.DeleteResponse{State: state}
	output = &first
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &first)
	if !first.Diagnostics.HasError() || first.State.Raw.IsNull() || reserves != 1 || physical != 0 {
		t.Fatalf("unknown delete lost ownership: %v", first.Diagnostics)
	}
	var held K8sNodePoolModel
	if d := first.State.Get(context.Background(), &held); d.HasError() {
		t.Fatal(d)
	}
	if held.DeletionRequestUUID.ValueString() != deletionUUID || held.ID.ValueInt64() != 7 {
		t.Fatal("unknown deletion checkpoint lost")
	}
	readable = true
	second := resource.DeleteResponse{State: first.State}
	output = &second
	r.Delete(context.Background(), resource.DeleteRequest{State: first.State}, &second)
	if second.Diagnostics.HasError() || reserves != 1 || submits != 2 || physical != 1 || legacy != 0 {
		t.Fatalf("unsafe known delete recovery: %v reserve/submit/physical/legacy=%d/%d/%d/%d", second.Diagnostics, reserves, submits, physical, legacy)
	}
}

func TestNodePoolLifecycleReadOnlyPositiveTypedGoneCanRemoveState(t *testing.T) {
	const id = "54f17ae4-2f21-4c02-aafd-a9c9adc7b455"
	for _, gone := range []bool{false, true} {
		t.Run(fmt.Sprintf("positive-gone-%v", gone), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				calls++
				if q.Method != http.MethodGet {
					t.Error("Read mutated operation")
				}
				v := poolLifecycleView(id, "SUCCESS", "CREATE", 7)
				if gone {
					v["exists"] = false
				} else {
					delete(v, "exists")
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": v})
			}))
			defer server.Close()
			r, request, _ := poolLifecycleFixture(t, server.URL)
			state := tfsdk.State(request.Plan)
			for name, value := range map[string]any{"id": int64(7), "request_uuid": id, "region": "TEST", "status": "SUCCESS", "node_subnet": int64(24)} {
				if d := state.SetAttribute(context.Background(), path.Root(name), value); d.HasError() {
					t.Fatal(d)
				}
			}
			response := resource.ReadResponse{State: state}
			r.Read(context.Background(), resource.ReadRequest{State: state}, &response)
			if response.Diagnostics.HasError() || calls != 1 || response.State.Raw.IsNull() != gone {
				t.Fatalf("wrong absence decision: %v calls=%d gone=%v", response.Diagnostics, calls, gone)
			}
			if !gone && !response.State.Raw.Equal(state.Raw) {
				t.Error("unverified read changed prior identity")
			}
		})
	}
}
