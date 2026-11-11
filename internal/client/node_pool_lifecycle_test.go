package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const poolTestUUID = "54f17ae4-2f21-4c02-aafd-a9c9adc7b455"

func poolOperationWire() map[string]any {
	return map[string]any{"contract": NodePoolLifecycleContract, "requestUuid": poolTestUUID, "clusterId": int64(313), "poolId": int64(7), "operation": "CREATE", "status": "RESERVED", "exists": true, "canRelease": false, "ready": false, "nodePoolName": "workers", "epoch": int64(91), "fingerprint": strings.Repeat("a", 64)}
}
func TestNodePoolOperationStrictIdentityAndProof(t *testing.T) {
	for _, tc := range []struct {
		name      string
		change    func(map[string]any)
		operation string
		valid     bool
	}{
		{name: "reserved", valid: true},
		{name: "success", change: func(v map[string]any) { v["status"] = "SUCCESS"; v["ready"] = true }, valid: true},
		{name: "historical create positively deleted", change: func(v map[string]any) { v["status"] = "SUCCESS"; v["ready"] = true; v["exists"] = false }, valid: true},
		{name: "negative create UUID", change: func(v map[string]any) {
			v["status"] = "NOT_SUBMITTED"
			v["poolId"] = nil
			v["exists"] = false
			v["canRelease"] = true
			v["ready"] = true
			v["nodePoolName"] = ""
		}, valid: true},
		{name: "positive delete", operation: "DELETE", change: func(v map[string]any) {
			v["operation"] = "DELETE"
			v["status"] = "SUCCESS"
			v["ready"] = true
			v["exists"] = false
		}, valid: true},
		{name: "positive historical delete needs no guessed name", operation: "DELETE", change: func(v map[string]any) {
			v["operation"] = "DELETE"
			v["status"] = "SUCCESS"
			v["ready"] = true
			v["exists"] = false
			v["nodePoolName"] = nil
		}, valid: true},
		{name: "cancelled delete retains live pool", operation: "DELETE", change: func(v map[string]any) {
			v["operation"] = "DELETE"
			v["status"] = "NOT_SUBMITTED"
			v["ready"] = true
			v["canRelease"] = true
		}, valid: true},
		{name: "wrong UUID", change: func(v map[string]any) { v["requestUuid"] = "00000000-0000-0000-0000-000000000000" }},
		{name: "wrong cluster", change: func(v map[string]any) { v["clusterId"] = 314 }},
		{name: "wrong operation", change: func(v map[string]any) { v["operation"] = "DELETE" }},
		{name: "missing exists", change: func(v map[string]any) { delete(v, "exists") }},
		{name: "missing ready", change: func(v map[string]any) { delete(v, "ready") }},
		{name: "missing cancellation proof", change: func(v map[string]any) { delete(v, "canRelease") }},
		{name: "missing canonical ID", change: func(v map[string]any) { v["poolId"] = nil }},
		{name: "zero epoch", change: func(v map[string]any) { v["epoch"] = 0 }},
		{name: "invalid fingerprint", change: func(v map[string]any) { v["fingerprint"] = "bad" }},
		{name: "unknown status", change: func(v map[string]any) { v["status"] = "RETRY" }},
		{name: "pending absence", change: func(v map[string]any) { v["exists"] = false }},
		{name: "pending ready", change: func(v map[string]any) { v["ready"] = true }},
		{name: "cancelled creation still exists", change: func(v map[string]any) { v["status"] = "NOT_SUBMITTED"; v["ready"] = true; v["canRelease"] = true }},
		{name: "delete success still exists", operation: "DELETE", change: func(v map[string]any) { v["operation"] = "DELETE"; v["status"] = "SUCCESS"; v["ready"] = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := poolOperationWire()
			if tc.change != nil {
				tc.change(body)
			}
			var calls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodGet || r.URL.Path != "/panel-main/api/v2/kubernetes/clusters/313/node-pool-operations/"+poolTestUUID {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": body})
			}))
			defer server.Close()
			c, err := New(Config{APIBaseURL: server.URL, APIKeyID: "test", APISecretKey: "test"})
			if err != nil {
				t.Fatal(err)
			}
			operation := tc.operation
			if operation == "" {
				operation = "CREATE"
			}
			_, err = c.GetNodePoolOperation(context.Background(), 313, poolTestUUID, operation, nil)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if calls != 1 {
				t.Fatalf("unexpected attempts %d", calls)
			}
		})
	}
}
func TestNodePoolOperationPublicGBBodyAndCapabilityGate(t *testing.T) {
	var reserve, legacy int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Region") != "TEST" || r.Header.Get("X-Project-Tag") != "test" {
			t.Error("operation scope lost")
		}
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"contract": NodePoolLifecycleContract, "callerUuid": true, "reserveBeforeSubmit": true, "recoverableSubmission": true, "uidPinnedDelete": true}})
			return
		}
		if r.URL.Path == "/panel-main/api/v2/kubernetes/clusters/313/node-pool-operations/"+poolTestUUID+"/reservations" {
			reserve++
			var body NodePoolOperationRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Operation != "CREATE" || body.PoolID != nil || body.Create == nil || body.Create.ClusterID != 313 || body.Create.WorkerRAM != 4 || body.Create.WorkerDiskSize != 20 {
				t.Errorf("wrong public GB contract: %+v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": poolOperationWire()})
			return
		}
		legacy++
		http.NotFound(w, r)
	}))
	defer server.Close()
	c, err := New(Config{APIBaseURL: server.URL, APIKeyID: "test", APISecretKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	opts := &RequestOpts{Region: "TEST", ProjectTag: "test"}
	if err = c.CheckNodePoolLifecycleAPI(context.Background(), 313, opts); err != nil {
		t.Fatal(err)
	}
	_, err = c.ReserveNodePoolOperation(context.Background(), 313, poolTestUUID, NodePoolOperationRequest{Operation: "CREATE", Create: &CreateNodePoolRequest{ClusterID: 313, NodePoolName: "workers", WorkerCPU: 2, WorkerRAM: 4, WorkerDiskSize: 20, WorkerReplicas: 1}}, opts)
	if err != nil || reserve != 1 || legacy != 0 {
		t.Fatalf("reserve=%d legacy=%d err=%v", reserve, legacy, err)
	}
}
