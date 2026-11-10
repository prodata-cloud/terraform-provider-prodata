package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestVolumeMoveContractRejectsAmbiguousOrWrongIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
		status int
	}{
		{"wrong contract", func(v map[string]any) { v["contract"] = "legacy" }, 200},
		{"wrong binding", func(v map[string]any) { v["id"] = 6 }, 200},
		{"wrong VM", func(v map[string]any) { v["vmId"] = 6 }, 200},
		{"wrong volume", func(v map[string]any) { v["volumeId"] = 6 }, 200},
		{"missing existence", func(v map[string]any) { delete(v, "exists") }, 200},
		{"unknown status", func(v map[string]any) { v["status"] = "something" }, 200},
		{"unknown direction", func(v map[string]any) { v["direction"] = "something" }, 200},
		{"false absence", func(v map[string]any) { v["exists"] = false }, 200},
		{"contradictory tombstone", func(v map[string]any) { v["status"] = "DELETED" }, 200},
		{"proxy failure with success body", func(v map[string]any) {}, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				value := map[string]any{"contract": VolumeAttachmentContract, "id": 9, "vmId": 7, "volumeId": 8, "exists": true, "direction": "ATTACH", "status": "SUCCESS"}
				tc.mutate(value)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": value})
			}))
			defer srv.Close()
			c, e := New(Config{APIBaseURL: srv.URL, APIKeyID: "fake", APISecretKey: "fake"})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = c.GetVolumeAttachment(context.Background(), 7, 8, 9, nil); e == nil {
				t.Fatal("ambiguous response accepted")
			}
		})
	}
}
func TestVolumeMoveCapabilitiesRequiredBeforeMutation(t *testing.T) {
	for _, body := range []string{`{}`, `{"contract":"volume-attachment-v1","reserveBeforeSubmit":true}`, `{"contract":"old","reserveBeforeSubmit":true,"recoverableSubmission":true}`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"data":` + body + `}`))
			}))
			defer srv.Close()
			c, e := New(Config{APIBaseURL: srv.URL, APIKeyID: "fake", APISecretKey: "fake"})
			if e != nil {
				t.Fatal(e)
			}
			if e = c.CheckVolumeAttachmentAPI(context.Background(), 7, nil); e == nil {
				t.Fatal("missing capability accepted")
			}
		})
	}
}

func TestVmVolumeInventoryNullCannotProveAbsence(t *testing.T) {
	for _, data := range []string{"null", "[]"} {
		t.Run(data, func(t *testing.T) {
			srv, _ := recordingServer(t, `{"success":true,"data":`+data+`}`)
			c := newTestClient(t, srv)
			disks, err := c.GetVmVolumes(context.Background(), 7, nil)
			if (err != nil) != (data == "null") {
				t.Fatalf("disks=%v err=%v", disks, err)
			}
		})
	}
}
func TestVolumeLegacyPhysicalIdentityAssertionsAreExplicitOnWire(t *testing.T) {
	expected := "ceph-pool:vm-100-disk-7"
	server, seen := recordingServer(t, `{"success":true,"data":{"contract":"volume-attachment-v1","id":9,"vmId":7,"volumeId":8,"exists":true,"direction":"ATTACH","status":"RESERVED"}}`)
	c := newTestClient(t, server)
	if _, err := c.ReserveVolumeAttachmentWithIdentity(context.Background(), 7, 8, expected, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeleteVolumeAttachmentWithIdentity(context.Background(), 7, 8, 9, expected, nil); err != nil {
		t.Fatal(err)
	}
	var body AttachVolumeRequest
	if err := json.Unmarshal([]byte((*seen)[0].body), &body); err != nil {
		t.Fatal(err)
	}
	if body.ExpectedSourceVolid != expected || body.VolumeID != 8 {
		t.Fatalf("body=%+v", body)
	}
	query, err := url.ParseQuery((*seen)[1].query)
	if err != nil || query.Get("expectedSourceVolid") != expected {
		t.Fatalf("query=%s err=%v", (*seen)[1].query, err)
	}
}
