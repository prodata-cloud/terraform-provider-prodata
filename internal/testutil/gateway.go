// Package testutil contains loopback fixtures used only by the local provider test suites.
package testutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// GatewayJournal adds a durable UUID journal to a fixture's existing local route model.
// Forwarded mutations execute at most once; reads and positive cancellation never activate.
func GatewayJournal(next http.Handler) http.Handler {
	type entry struct {
		vmPath, uuid string
		network      int64
		body         map[string]any
		forecast     map[string]any
		status       string
		kind         string
		original     string
	}
	var mu sync.Mutex
	operations := map[string]*entry{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		at := strings.Index(r.URL.Path, "/gateway-operations/")
		if at < 0 {
			next.ServeHTTP(w, r)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		ok := func(v any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": v})
		}
		tail := strings.TrimPrefix(r.URL.Path[at:], "/gateway-operations/")
		if tail == "capabilities" {
			ok(map[string]any{"contract": "vm-gateway-operation-v1", "reserveBeforeSubmit": true, "recoverableSubmission": true, "powerChildrenBeforeSubmit": true, "cancelTombstone": true})
			return
		}
		parts := strings.Split(tail, "/")
		uuid := parts[0]
		e := operations[uuid]
		var network int64
		_, _ = fmt.Sscan(r.URL.Query().Get("networkId"), &network)
		vmPath := r.URL.Path[:at]
		render := func() {
			var exists any
			release := e.status == "NOT_SUBMITTED"
			if release {
				exists = false
			} else if e.status == "SUCCESS" {
				exists = true
			}
			var id any
			if e.forecast != nil {
				id = e.forecast["interfaceId"]
			}
			var vmID int64
			_, _ = fmt.Sscan(vmPath[strings.LastIndex(vmPath, "/")+1:], &vmID)
			ok(map[string]any{"contract": "vm-gateway-operation-v1", "requestUuid": uuid, "vmId": vmID, "networkId": e.network, "interfaceId": id, "status": e.status, "exists": exists, "canRelease": release, "result": e.forecast, "operation": e.kind, "guestOperationUuid": r.Header.Get("X-Prodata-Guest-Operation-UUID")})
		}
		forward := func(method string, body map[string]any, dry bool) (map[string]any, bool) {
			encoded, _ := json.Marshal(body)
			clone := r.Clone(r.Context())
			u := *r.URL
			u.Path = vmPath + "/gateway"
			q := u.Query()
			q.Del("networkId")
			if dry {
				q.Set("dryRun", "true")
			} else {
				q.Del("dryRun")
			}
			u.RawQuery = q.Encode()
			clone.URL = &u
			clone.Method = method
			clone.Body = http.NoBody
			if method != "GET" {
				clone.Body = ioNopCloser{bytes.NewReader(encoded)}
			}
			rec := httptest.NewRecorder()
			next.ServeHTTP(rec, clone)
			var env struct {
				Success bool           `json:"success"`
				Data    map[string]any `json:"data"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &env)
			if rec.Code >= 400 || !env.Success {
				for k, v := range rec.Header() {
					w.Header()[k] = v
				}
				w.WriteHeader(rec.Code)
				_, _ = w.Write(rec.Body.Bytes())
				return nil, false
			}
			return env.Data, true
		}
		if e != nil && (e.vmPath != vmPath || e.network != network) {
			http.Error(w, "scope mismatch", http.StatusConflict)
			return
		}
		if r.Method == "POST" && len(parts) == 2 && parts[1] == "reservations" {
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				http.Error(w, "bad reservation", 400)
				return
			}
			encoded, _ := json.Marshal(body)
			if e != nil {
				if e.status != "NOT_SUBMITTED" && e.original != string(encoded) {
					http.Error(w, "immutable UUID spec", http.StatusConflict)
					return
				}
				render()
				return
			}
			forecast, valid := forward("PUT", body, true)
			if !valid {
				return
			}
			kind := "SET_GATEWAY"
			if body["clear"] == true {
				kind = "CLEAR_GATEWAY"
			}
			e = &entry{vmPath: vmPath, uuid: uuid, network: network, body: body, forecast: forecast, status: "RESERVED", kind: kind, original: string(encoded)}
			body["expectedInterfaceId"] = forecast["interfaceId"]
			operations[uuid] = e
			render()
			return
		}
		if r.Method == "DELETE" {
			if e == nil {
				e = &entry{vmPath: vmPath, uuid: uuid, network: network, status: "NOT_SUBMITTED", kind: "CANCELLED"}
				operations[uuid] = e
			} else if e.status != "SUCCESS" {
				e.status = "NOT_SUBMITTED"
				e.forecast = nil
			}
			render()
			return
		}
		if e == nil {
			http.Error(w, "unknown operation", 404)
			return
		}
		if r.Method == "POST" && len(parts) == 2 && parts[1] == "submit" && e.status == "RESERVED" {
			// The baseline fixtures' rejected writes have no effect. More detailed lost-ACK
			// tests use their own explicit in-flight/terminal proof instead of this adapter.
			e.status = "SUBMITTING"
			if e.forecast["changed"] == false {
				actual, valid := forward("GET", nil, false)
				if !valid {
					return
				}
				if actual["reconcileRequired"] != true {
					e.status = "SUCCESS"
					render()
					return
				}
			}
			result, valid := forward("PUT", e.body, false)
			if !valid {
				return
			}
			e.forecast = result
			e.status = "SUCCESS"
		}
		render()
	})
}

type ioNopCloser struct{ *bytes.Reader }

func (ioNopCloser) Close() error { return nil }
