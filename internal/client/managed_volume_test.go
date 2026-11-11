package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestManagedVolumeRequiresExplicitStableIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		wantError  bool
	}{
		{"free", `{"identityVersion":"user-disk-v1","id":9,"exists":true,"name":"disk","type":"SSD","size":10,"inUse":false}`, 200, false},
		{"attached", `{"identityVersion":"user-disk-v1","id":9,"exists":true,"name":"disk","type":"SSD","size":10,"inUse":true,"attachedId":77,"attachmentId":515}`, 200, false},
		{"confirmed absent", `{"identityVersion":"user-disk-v1","id":9,"exists":false}`, 200, false},
		{"legacy response", `{"id":9,"name":"disk","size":10}`, 200, true},
		{"wrong id", `{"identityVersion":"user-disk-v1","id":515,"exists":false}`, 200, true},
		{"missing existence", `{"identityVersion":"user-disk-v1","id":9}`, 200, true},
		{"contradictory absence", `{"identityVersion":"user-disk-v1","id":9,"exists":false,"inUse":true,"attachedId":77,"attachmentId":515}`, 200, true},
		{"invalid attachment identity", `{"identityVersion":"user-disk-v1","id":9,"exists":true,"name":"disk","type":"SSD","size":10,"inUse":true,"attachedId":0,"attachmentId":515}`, 200, true},
		{"incomplete attachment", `{"identityVersion":"user-disk-v1","id":9,"exists":true,"name":"disk","type":"SSD","size":10,"inUse":true}`, 200, true},
		{"old server route404", `null`, 404, true},
		{"server failure", `null`, 500, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(tc.status, `{"success":true,"data":`+tc.body+`}`)
			defer srv.Close()
			volume, err := newTestClient(t, srv).GetManagedVolume(context.Background(), 9, nil)
			if (err != nil) != tc.wantError {
				t.Fatalf("volume=%+v error=%v, want error %v", volume, err, tc.wantError)
			}
		})
	}
}

func TestManagedVolumeMutationsRequireConfirmedIdentity(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		for _, valid := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/valid=%t", method, valid), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != method || r.URL.Path != "/panel-main/api/v2/volumes/by-user-disk/9" ||
						r.Header.Get("X-Project-Tag") != "resource-project" || r.Header.Get("X-Region") != "OTHER" {
						t.Errorf("wrong mutation target %s %s", r.Method, r.URL.Path)
					}
					w.Header().Set("Content-Type", "application/json")
					data := `{"identityVersion":"user-disk-v1","id":9,"exists":false}`
					if method == http.MethodPut {
						data = `{"identityVersion":"user-disk-v1","id":9,"exists":true,"name":"renamed","type":"SSD","size":10,"inUse":false}`
					}
					if !valid {
						data = `{"id":515,"name":"wrong disk"}`
					}
					_, _ = fmt.Fprintf(w, `{"success":true,"data":%s}`, data)
				}))
				defer srv.Close()
				c := newTestClient(t, srv)
				var err error
				if method == http.MethodPut {
					_, err = c.UpdateManagedVolume(context.Background(), 9, UpdateVolumeRequest{Name: "renamed", Region: "OTHER", ProjectTag: "resource-project"})
				} else {
					err = c.DeleteManagedVolume(context.Background(), 9, &RequestOpts{Region: "OTHER", ProjectTag: "resource-project"})
				}
				if (err == nil) != valid {
					t.Fatalf("error=%v, want valid=%t", err, valid)
				}
			})
		}
	}
}

func TestManagedVolumeInventoryReportsUnverifiedIdsSeparately(t *testing.T) {
	srv := newTestServer(200, `{"success":true,"data":{"identityVersion":"user-disk-v1","volumes":[],"unverifiedVolumeIds":[9]}}`)
	defer srv.Close()
	c := newTestClient(t, srv)
	inventory, err := c.GetManagedVolumeInventory(context.Background(), nil)
	if err != nil || len(inventory.Volumes) != 0 || len(inventory.UnverifiedVolumeIDs) != 1 || inventory.UnverifiedVolumeIDs[0] != 9 {
		t.Fatalf("inventory=%+v error=%v", inventory, err)
	}
	if _, err := c.GetManagedVolumes(context.Background(), nil); err == nil {
		t.Fatal("cleanup accepted an unverified volume list")
	}
}
