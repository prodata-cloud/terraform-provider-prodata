package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProtectionCreationRequiresRecoverableUpstreamBeforeAllocation(t *testing.T) {
	for _, field := range []string{"", `,"recoverableSubmission":false`, `,"recoverableSubmission":null`, `,"recoverableSubmission":true`} {
		t.Run(field, func(t *testing.T) {
			mutations := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet && r.URL.Path == "/panel-main/api/v2/vm-protection/capabilities" {
					_, _ = fmt.Fprintf(w, `{"success":true,"data":{"contract":"vm-protection-reservation-v1","reserveBeforeSubmit":true,"singleSubmit":true,"restoreIdentityBeforeSubmit":true%s}}`, field)
					return
				}
				mutations++
				_, _ = fmt.Fprint(w, `{"success":true,"data":{"contract":"vm-protection-reservation-v1","id":8,"vmId":5,"present":true,"status":"RESERVED"}}`)
			}))
			defer server.Close()
			_, err := newTestClient(t, server).SaveVmProtection(context.Background(), 5, "snapshots", 0, VmProtectionRequest{Name: "point"}, nil)
			want := field == `,"recoverableSubmission":true`
			if (err == nil) != want {
				t.Fatalf("success=%v, want %v: %v", err == nil, want, err)
			}
			if !want && mutations != 0 || want && mutations != 1 {
				t.Fatalf("mutations=%d, recoverable=%v", mutations, want)
			}
		})
	}
}
