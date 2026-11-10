package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func localOperationWire() map[string]any {
	return map[string]any{"contract": LocalNetworkAttachmentContract, "id": 91, "vmId": 7, "networkId": 42, "exists": true, "operation": "ATTACH_LOCAL_NIC", "status": "SUCCESS", "canRelease": false, "ip": "192.0.2.5", "mac": "BC:24:11:AA:BB:CC", "netIndex": 1, "pveFullName": "net1"}
}
func TestLocalInterfaceOperationRequiresIdentityAndPositiveAbsence(t *testing.T) {
	for _, mutate := range []func(map[string]any){func(v map[string]any) { v["contract"] = "old" }, func(v map[string]any) { v["id"] = 99 }, func(v map[string]any) { v["vmId"] = 99 }, func(v map[string]any) { v["networkId"] = 99 }, func(v map[string]any) { delete(v, "exists") }, func(v map[string]any) { v["exists"] = false }, func(v map[string]any) { v["status"] = "garbage" }, func(v map[string]any) { delete(v, "netIndex") }, func(v map[string]any) { v["pveFullName"] = "net2" }} {
		v := localOperationWire()
		mutate(v)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": v})
		}))
		c := newTestClient(t, srv)
		if _, err := c.GetLocalNetworkAttachment(context.Background(), 7, 42, 91, nil); err == nil {
			t.Fatalf("accepted %+v", v)
		}
		srv.Close()
	}
}
func TestLocalInterfaceDomainErrorsCannotReplaceDeletionProof(t *testing.T) {
	for _, body := range []string{`<html>not found</html>`, `{"success":false,"errors":[{"code":633,"message":"VM not found"}]}`, `{"success":false,"errors":[{"code":703,"message":"NIC not found"}]}`, `{"success":false,"errors":[{"code":601,"message":"blocked user"}]}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) { w.WriteHeader(404); _, _ = w.Write([]byte(body)) }))
		c := newTestClient(t, srv)
		if _, err := c.GetLocalNetworkAttachment(context.Background(), 7, 42, 91, nil); err == nil {
			t.Fatal("domain error substituted for physical deletion proof")
		}
		srv.Close()
	}
}
func TestConfirmedVmDeletionAllowsMinimalLocalInterfaceTombstone(t *testing.T) {
	srv, _ := recordingServer(t, `{"success":true,"data":{"contract":"local-network-attachment-v1","id":91,"vmId":7,"networkId":null,"exists":false,"operation":"DETACH_LOCAL_NIC","status":"DELETED","canRelease":false}}`)
	c := newTestClient(t, srv)
	v, err := c.GetLocalNetworkAttachment(context.Background(), 7, 42, 91, nil)
	if err != nil || v.Exists == nil || *v.Exists {
		t.Fatalf("%+v %v", v, err)
	}
}
