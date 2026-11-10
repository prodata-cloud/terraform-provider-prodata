package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGatewayOperationRejectsIncompleteAndUncorrelatedProof(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	cases := map[string]func(map[string]any, map[string]any){
		"wrong request":             func(w, _ map[string]any) { w["requestUuid"] = "22222222-2222-4222-8222-222222222222" },
		"wrong vm":                  func(w, _ map[string]any) { w["vmId"] = 8 },
		"wrong network":             func(w, _ map[string]any) { w["networkId"] = 43 },
		"wrong canonical interface": func(_, r map[string]any) { r["interfaceId"] = 92 },
		"missing changed":           func(_, r map[string]any) { delete(r, "changed") },
		"missing otherGateway":      func(_, r map[string]any) { delete(r, "previousGatewayOnAnotherInterface") },
		"missing guest flag":        func(_, r map[string]any) { delete(r, "requiresGuestConfiguration") },
		"untyped absence":           func(w, _ map[string]any) { w["exists"] = false; w["canRelease"] = true },
		"null completed presence":   func(w, _ map[string]any) { w["exists"] = nil },
		"missing desired route":     func(_, r map[string]any) { r["gateway"] = nil },
		"unknown status":            func(w, _ map[string]any) { w["status"] = "UNKNOWN" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			result := map[string]any{"interfaceId": 91, "networkId": 42, "gateway": "192.0.2.1", "changed": true, "previousGatewayOnAnotherInterface": false, "requiresGuestConfiguration": false}
			wire := map[string]any{"contract": GatewayOperationContract, "requestUuid": id, "vmId": 7, "networkId": 42, "interfaceId": 91, "operation": "SET_GATEWAY", "status": "SUCCESS", "exists": true, "canRelease": false, "result": result}
			change(wire, result)
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": wire})
			}))
			defer s.Close()
			c, e := New(Config{APIBaseURL: s.URL, APIKeyID: "fake", APISecretKey: "fake"})
			if e != nil {
				t.Fatal(e)
			}
			if out, e := c.ReadGatewayOperation(context.Background(), 7, 42, id, nil); e == nil {
				t.Fatalf("accepted malformed proof %+v", out)
			}
		})
	}
}
func TestGatewayOperationAcceptsMinimalPositiveCancellation(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"contract": GatewayOperationContract, "requestUuid": id, "vmId": 7, "networkId": 42, "interfaceId": nil, "operation": "CANCELLED", "status": "NOT_SUBMITTED", "exists": false, "canRelease": true}})
	}))
	defer s.Close()
	c, _ := New(Config{APIBaseURL: s.URL, APIKeyID: "fake", APISecretKey: "fake"})
	out, e := c.CancelGatewayOperation(context.Background(), 7, 42, id, nil)
	if e != nil || !gatewayOperationCancelled(out) {
		t.Fatalf("out=%+v err=%v", out, e)
	}
}
