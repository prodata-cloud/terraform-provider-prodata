package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

func powerCapabilitiesWire() map[string]any {
	return map[string]any{"contract": VmPowerOperationContract, "callerUuid": true, "claimBeforeWrite": true, "readNeverActivates": true, "resolveTombstone": true, "recoverablePowerChildren": true, "stopCheckpoint": true, "supportedActions": []string{"START", "REBOOT", "RESET", "RESUME", "STOP", "SHUTDOWN"}}
}

func TestVmPowerCapabilityIsScopedAndIndependentOfGuestSupport(t *testing.T) {
	body, err := json.Marshal(map[string]any{"success": true, "data": powerCapabilitiesWire()})
	if err != nil {
		t.Fatal(err)
	}
	server, seen := recordingServer(t, string(body))
	c := newTestClient(t, server)
	if err := c.RequireVmPowerOperations(context.Background(), 7, &RequestOpts{Region: "TEST", ProjectTag: "selected"}); err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 || (*seen)[0].method != http.MethodGet || (*seen)[0].path != "/panel-main/api/v2/vms/7/power-operations/capabilities" {
		t.Fatalf("unexpected power admission: %+v", *seen)
	}
	query, err := url.ParseQuery((*seen)[0].query)
	if err != nil {
		t.Fatal(err)
	}
	if query.Get("region") != "TEST" || query.Get("projectTag") != "selected" {
		t.Fatalf("power capability lost scope: %v", query)
	}
}

func TestVmPowerCapabilityRequiresEveryRecoveryGuarantee(t *testing.T) {
	for _, field := range []string{"contract", "callerUuid", "claimBeforeWrite", "readNeverActivates", "resolveTombstone", "recoverablePowerChildren", "stopCheckpoint"} {
		for _, mode := range []string{"missing", "false"} {
			t.Run(field+"/"+mode, func(t *testing.T) {
				wire := powerCapabilitiesWire()
				if mode == "missing" {
					delete(wire, field)
				} else if field == "contract" {
					wire[field] = "other-contract"
				} else {
					wire[field] = false
				}
				body, err := json.Marshal(map[string]any{"success": true, "data": wire})
				if err != nil {
					t.Fatal(err)
				}
				server, seen := recordingServer(t, string(body))
				if err := newTestClient(t, server).RequireVmPowerOperations(context.Background(), 7, nil); err == nil || len(*seen) != 1 {
					t.Fatalf("incomplete power contract accepted: calls=%d err=%v", len(*seen), err)
				}
			})
		}
	}
}

func TestVmPowerCapabilityRequiresRecoverableStopAndStartActions(t *testing.T) {
	for _, omitted := range []string{"START", "REBOOT", "RESET", "RESUME", "STOP", "SHUTDOWN"} {
		t.Run(omitted, func(t *testing.T) {
			wire := powerCapabilitiesWire()
			var actions []string
			for _, action := range []string{"START", "REBOOT", "RESET", "RESUME", "STOP", "SHUTDOWN"} {
				if action != omitted {
					actions = append(actions, action)
				}
			}
			wire["supportedActions"] = actions
			body, err := json.Marshal(map[string]any{"success": true, "data": wire})
			if err != nil {
				t.Fatal(err)
			}
			server, _ := recordingServer(t, string(body))
			if err := newTestClient(t, server).RequireVmPowerOperations(context.Background(), 7, nil); err == nil {
				t.Fatalf("missing %s recovery support was accepted", omitted)
			}
		})
	}
}
