package client

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func clusterCreationWire() map[string]any {
	return map[string]any{"contract": ClusterCreationContract, "requestUuid": poolTestUUID, "clusterId": 313, "status": "RESERVED", "exists": true, "canRelease": false, "ready": false, "epoch": 91, "fingerprint": strings.Repeat("a", 64), "name": "tfc"}
}
func TestClusterCreationRequiresExactIdentityAndPositiveProof(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(map[string]any)
		valid  bool
	}{
		{"reserved", func(map[string]any) {}, true},
		{"success", func(v map[string]any) { v["status"] = "SUCCESS"; v["ready"] = true }, true},
		{"historically deleted", func(v map[string]any) { v["status"] = "SUCCESS"; v["ready"] = true; v["exists"] = false }, true},
		{"negative UUID", func(v map[string]any) {
			v["status"] = "NOT_SUBMITTED"
			v["clusterId"] = nil
			v["name"] = nil
			v["exists"] = false
			v["canRelease"] = true
			v["ready"] = true
		}, true},
		{"wrong UUID", func(v map[string]any) { v["requestUuid"] = "other" }, false},
		{"wrong contract", func(v map[string]any) { v["contract"] = "legacy" }, false},
		{"missing canonical ID", func(v map[string]any) { delete(v, "clusterId") }, false},
		{"missing exists", func(v map[string]any) { delete(v, "exists") }, false},
		{"missing release", func(v map[string]any) { delete(v, "canRelease") }, false},
		{"missing ready", func(v map[string]any) { delete(v, "ready") }, false},
		{"missing epoch", func(v map[string]any) { delete(v, "epoch") }, false},
		{"missing fingerprint", func(v map[string]any) { delete(v, "fingerprint") }, false},
		{"pending absence", func(v map[string]any) { v["exists"] = false }, false},
		{"pending ready", func(v map[string]any) { v["ready"] = true }, false},
		{"cancelled but exists", func(v map[string]any) { v["status"] = "NOT_SUBMITTED"; v["ready"] = true; v["canRelease"] = true }, false},
		{"unknown status", func(v map[string]any) { v["status"] = "UNKNOWN" }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := clusterCreationWire()
			test.change(wire)
			body, e := json.Marshal(map[string]any{"success": true, "data": wire})
			if e != nil {
				t.Fatal(e)
			}
			server, seen := recordingServer(t, string(body))
			_, err := newTestClient(t, server).GetClusterCreation(context.Background(), poolTestUUID, nil)
			if (err == nil) != test.valid || len(*seen) != 1 {
				t.Fatalf("valid=%t calls=%d err=%v", test.valid, len(*seen), err)
			}
		})
	}
}
func TestClusterCreationPublicBodyAndScope(t *testing.T) {
	body, e := json.Marshal(map[string]any{"success": true, "data": clusterCreationWire()})
	if e != nil {
		t.Fatal(e)
	}
	server, seen := recordingServer(t, string(body))
	request := CreateClusterRequest{ClusterName: "tfc", KuberVersion: "v1.31.4", MasterNodeConfigID: 7, LocalNetID: 42, PodSubnet: "10.244.0.0/16"}
	_, err := newTestClient(t, server).ReserveClusterCreation(context.Background(), poolTestUUID, request, &RequestOpts{Region: "TEST", ProjectTag: "selected"})
	if err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 || (*seen)[0].method != http.MethodPost || (*seen)[0].path != "/panel-main/api/v2/kubernetes/cluster-operations/"+poolTestUUID+"/reservations" {
		t.Fatalf("wrong route: %+v", *seen)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte((*seen)[0].body), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["clusterName"] != "tfc" || decoded["masterNodeConfigId"] != float64(7) || decoded["localNetId"] != float64(42) || decoded["create"] != nil || decoded["namespace"] != nil {
		t.Fatalf("unsafe public payload %+v", decoded)
	}
}
func TestClusterCreationCapabilitiesFailClosed(t *testing.T) {
	for _, missing := range []string{"", "contract", "callerUuid", "reserveBeforeSubmit", "recoverableSubmission", "resolveTombstone", "compensatingDeleteFence", "journalIdentityFence"} {
		t.Run(missing, func(t *testing.T) {
			wire := map[string]any{"contract": ClusterCreationContract, "callerUuid": true, "reserveBeforeSubmit": true, "recoverableSubmission": true, "resolveTombstone": true, "compensatingDeleteFence": true, "journalIdentityFence": true}
			delete(wire, missing)
			body, e := json.Marshal(map[string]any{"success": true, "data": wire})
			if e != nil {
				t.Fatal(e)
			}
			server, _ := recordingServer(t, string(body))
			err := newTestClient(t, server).RequireClusterCreationAPI(context.Background(), nil)
			if (err == nil) != (missing == "") {
				t.Fatalf("missing=%s err=%v", missing, err)
			}
		})
	}
}
