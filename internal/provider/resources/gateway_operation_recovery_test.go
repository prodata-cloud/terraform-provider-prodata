package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"terraform-provider-prodata/internal/client"
	"testing"
	"time"
)

// This is an independent journal model, without the old-endpoint fixture adapter.
// It distinguishes accepted physical work, lost HTTP acknowledgements and positive tombstones.
type strictGatewayPanel struct {
	submitBusy                                                  int
	missingCoreAfterBusy                                        bool
	mu                                                          sync.Mutex
	id, status                                                  string
	body                                                        client.ChangeVmGatewayRequest
	gateway                                                     *string
	reserveLost, activateLost, unknown, missingRead, capMissing bool
	reserves, submits, reads, cancels, physical, power          int
	beforeReserve                                               func(string)
}

type gatewayPanelStats struct{ reserves, submits, reads, cancels, physical, power int }

func (p *strictGatewayPanel) stats() gatewayPanelStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return gatewayPanelStats{p.reserves, p.submits, p.reads, p.cancels, p.physical, p.power}
}
func (p *strictGatewayPanel) serve(w http.ResponseWriter, q *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ok := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": v}) }
	if strings.HasSuffix(q.URL.Path, "/status") {
		ok(map[string]any{"id": 7, "status": "STOPPED"})
		return
	}
	if strings.HasSuffix(q.URL.Path, "/start") || strings.HasSuffix(q.URL.Path, "/stop") {
		p.power++
		ok(nil)
		return
	}
	if strings.HasSuffix(q.URL.Path, "/gateway") {
		if q.Method != "GET" {
			http.Error(w, "raw physical writer forbidden", 500)
			return
		}
		ok(map[string]any{"interfaceId": 91, "networkId": 42, "gateway": p.gateway, "present": p.gateway != nil, "pending": false, "reconcileRequired": false, "otherGatewayActive": false, "requiresGuestConfiguration": false})
		return
	}
	if strings.HasSuffix(q.URL.Path, "/capabilities") {
		if p.capMissing {
			http.NotFound(w, q)
			return
		}
		ok(map[string]any{"contract": client.GatewayOperationContract, "reserveBeforeSubmit": true, "recoverableSubmission": true, "powerChildrenBeforeSubmit": true, "cancelTombstone": true})
		return
	}
	parts := strings.Split(strings.Split(q.URL.Path, "/gateway-operations/")[1], "/")
	id := parts[0]
	result := func() map[string]any {
		var exists any
		release := p.status == "NOT_SUBMITTED"
		if release {
			exists = false
		} else if p.status == "SUCCESS" {
			exists = true
		}
		kind := "SET_GATEWAY"
		answer := p.body.Gateway
		changed := p.gateway == nil || answer == nil || *p.gateway != *answer
		if p.body.Clear {
			kind = "CLEAR_GATEWAY"
			changed = p.gateway != nil && p.body.ExpectedGateway != nil && *p.gateway == *p.body.ExpectedGateway
			answer = p.gateway
			if changed {
				answer = nil
			}
		}
		return map[string]any{"contract": client.GatewayOperationContract, "requestUuid": id, "vmId": 7, "networkId": 42, "interfaceId": 91, "status": p.status, "exists": exists, "canRelease": release, "operation": kind, "result": map[string]any{"interfaceId": 91, "networkId": 42, "gateway": answer, "changed": changed, "previousGatewayOnAnotherInterface": false, "requiresGuestConfiguration": false}}
	}
	if strings.HasSuffix(q.URL.Path, "/reservations") {
		p.reserves++
		if p.beforeReserve != nil {
			p.beforeReserve(id)
		}
		if p.id == "" {
			p.id = id
			p.status = "RESERVED"
			_ = json.NewDecoder(q.Body).Decode(&p.body)
		} else if p.id != id && p.status != "SUBMITTING" {
			p.id = id
			p.status = "RESERVED"
			_ = json.NewDecoder(q.Body).Decode(&p.body)
		}
		if p.reserveLost {
			w.WriteHeader(502)
			return
		}
		ok(result())
		return
	}
	if q.Method == "GET" {
		p.reads++
		if p.missingRead || p.id != id {
			http.NotFound(w, q)
			return
		}
		ok(result())
		return
	}
	if q.Method == "DELETE" {
		p.cancels++
		if p.status != "SUCCESS" && !p.unknown {
			p.id = id
			p.status = "NOT_SUBMITTED"
		}
		ok(result())
		return
	}
	if strings.HasSuffix(q.URL.Path, "/submit") {
		p.submits++
		if p.submitBusy > 0 {
			p.submitBusy--
			p.missingCoreAfterBusy = true
			p.status = "SUBMITTING"
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if p.missingCoreAfterBusy {
			p.missingCoreAfterBusy = false
			p.status = "RESERVED"
		}
		if p.status == "RESERVED" {
			p.status = "SUBMITTING"
			p.physical++
			if !p.unknown {
				p.status = "SUCCESS"
				if p.body.Clear {
					if p.gateway != nil && p.body.ExpectedGateway != nil && *p.gateway == *p.body.ExpectedGateway {
						p.gateway = nil
					}
				} else {
					p.gateway = p.body.Gateway
				}
			}
		}
		if p.activateLost {
			w.WriteHeader(502)
			return
		}
		ok(result())
		return
	}
	http.Error(w, "unexpected request", 500)
}
func strictGatewayClient(t *testing.T, p *strictGatewayPanel) *client.Client {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(s.Close)
	c, e := client.New(client.Config{APIBaseURL: s.URL, APIKeyID: "fake", APISecretKey: "fake", Region: "TEST", ProjectTag: "proj"})
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func TestGatewayCallerUuidCheckpointSurvivesLostReservationAck(t *testing.T) {
	p := &strictGatewayPanel{reserveLost: true}
	r := &VmGatewayResource{client: strictGatewayClient(t, p), poll: time.Millisecond}
	plan, empty := gatewayRaw(t, nil)
	out := resource.CreateResponse{State: empty}
	p.beforeReserve = func(id string) {
		var checkpoint VmGatewayResourceModel
		if d := out.State.Get(context.Background(), &checkpoint); d.HasError() || out.State.Raw.IsNull() || !out.State.Raw.IsFullyKnown() || checkpoint.PlatformOperationUUID.ValueString() != id {
			t.Errorf("reservation sent before durable fully known checkpoint: %v %v", out.State.Raw, d)
		}
	}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &out)
	if !out.Diagnostics.HasError() || out.State.Raw.IsNull() || p.stats().physical != 0 || p.stats().power != 0 {
		t.Fatalf("physical=%d power=%d state=%v diag=%v", p.stats().physical, p.stats().power, out.State.Raw, out.Diagnostics)
	}
	p.mu.Lock()
	p.beforeReserve = nil
	p.mu.Unlock()
	removed := resource.DeleteResponse{State: out.State}
	r.Delete(context.Background(), resource.DeleteRequest{State: out.State}, &removed)
	if removed.Diagnostics.HasError() || p.stats().cancels != 1 || p.stats().physical != 0 || p.stats().power != 0 {
		t.Fatalf("cancel=%d physical=%d power=%d diag=%v", p.stats().cancels, p.stats().physical, p.stats().power, removed.Diagnostics)
	}
}
func TestGatewayLostSubmitAckReadsExactUuidAndNeverReissuesPhysicalWrite(t *testing.T) {
	p := &strictGatewayPanel{activateLost: true}
	r := &VmGatewayResource{client: strictGatewayClient(t, p), poll: time.Millisecond}
	plan, empty := gatewayRaw(t, nil)
	out := resource.CreateResponse{State: empty}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &out)
	if out.Diagnostics.HasError() || p.stats().physical != 1 || p.stats().submits != 1 || p.stats().reads != 1 {
		t.Fatalf("physical=%d submits=%d reads=%d diag=%v", p.stats().physical, p.stats().submits, p.stats().reads, out.Diagnostics)
	}
	var state VmGatewayResourceModel
	out.State.Get(context.Background(), &state)
	if state.PlatformOperationStatus.ValueString() != "SUCCESS" || state.OwnedGateway.ValueString() != "192.0.2.1" {
		t.Fatal(state)
	}
}
func TestGatewayUnknownPhysicalOutcomeRetainsUuidAndDeleteCannotReleaseIt(t *testing.T) {
	p := &strictGatewayPanel{unknown: true}
	r := &VmGatewayResource{client: strictGatewayClient(t, p), poll: time.Millisecond}
	plan, empty := gatewayRaw(t, nil)
	out := resource.CreateResponse{State: empty}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &out)
	if !out.Diagnostics.HasError() || p.stats().physical != 1 || out.State.Raw.IsNull() {
		t.Fatalf("physical=%d state=%v diag=%v", p.stats().physical, out.State.Raw, out.Diagnostics)
	}
	var prior VmGatewayResourceModel
	out.State.Get(context.Background(), &prior)
	reads := p.stats().reads
	reserved := p.stats().reserves
	// A cancelled Create HTTP call may already be in flight on the original server. Observe
	// Read/Delete through a separate endpoint sharing the same journal, so their own methods
	// are distinguished from a late request issued by the earlier mutation waiter.
	var readOrDeletePosts atomic.Int64
	readonlyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.Method == http.MethodPost {
			readOrDeletePosts.Add(1)
		}
		p.serve(w, q)
	}))
	t.Cleanup(readonlyServer.Close)
	readonlyClient, err := client.New(client.Config{APIBaseURL: readonlyServer.URL, APIKeyID: "fake", APISecretKey: "fake", Region: "TEST", ProjectTag: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	r.client = readonlyClient
	observed := resource.ReadResponse{State: out.State}
	r.Read(context.Background(), resource.ReadRequest{State: out.State}, &observed)
	removed := resource.DeleteResponse{State: observed.State}
	r.Delete(context.Background(), resource.DeleteRequest{State: observed.State}, &removed)
	var kept VmGatewayResourceModel
	removed.State.Get(context.Background(), &kept)
	if !removed.Diagnostics.HasError() || removed.State.Raw.IsNull() || kept.PlatformOperationUUID != prior.PlatformOperationUUID || p.stats().physical != 1 || p.stats().reserves != reserved || readOrDeletePosts.Load() != 0 || p.stats().reads <= reads {
		t.Fatalf("state=%+v physical=%d diag=%v", kept, p.stats().physical, removed.Diagnostics)
	}
}
func TestGatewayKnownPendingProxy404DoesNotFallbackOrForgetOwnership(t *testing.T) {
	p := &strictGatewayPanel{missingRead: true}
	r := &VmGatewayResource{client: strictGatewayClient(t, p)}
	state := gatewayState(t, "192.0.2.1", "192.0.2.1")
	state.SetAttribute(context.Background(), path.Root("platform_operation_uuid"), "11111111-1111-4111-8111-111111111111")
	state.SetAttribute(context.Background(), path.Root("platform_operation_status"), "SUBMITTING")
	out := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &out)
	if out.State.Raw.IsNull() || p.stats().reads != 1 || p.stats().reserves != 0 || p.stats().submits != 0 || p.stats().cancels != 0 {
		t.Fatalf("state=%v panel=%+v diag=%v", out.State.Raw, p, out.Diagnostics)
	}
}
func TestGatewayMissingCapabilitiesCannotAllocateIntentOrStopVm(t *testing.T) {
	p := &strictGatewayPanel{capMissing: true}
	r := &VmGatewayResource{client: strictGatewayClient(t, p)}
	data := gatewayModel()
	gateway := "192.0.2.1"
	_, diags := r.apply(context.Background(), &data, client.ChangeVmGatewayRequest{NetworkID: 42, Gateway: &gateway})
	if !diags.HasError() || data.PlatformOperationUUID.ValueString() != "" || p.stats().reserves != 0 || p.stats().physical != 0 || p.stats().power != 0 {
		t.Fatalf("state=%+v panel=%+v diag=%v", data, p, diags)
	}
}
func TestGatewayReadAbsenceRequiresTypedDeletedVmProof(t *testing.T) {
	for _, code := range []int{0, 633, 703, 601, 714} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				w.WriteHeader(404)
				if code > 0 {
					fmt.Fprintf(w, `{"success":false,"errors":[{"code":%d}]}`, code)
				}
			}))
			defer s.Close()
			c, _ := client.New(client.Config{APIBaseURL: s.URL, APIKeyID: "fake", APISecretKey: "fake"})
			r := &VmGatewayResource{client: c}
			prior := gatewayState(t, "192.0.2.1", "192.0.2.1")
			out := resource.ReadResponse{State: prior}
			r.Read(context.Background(), resource.ReadRequest{State: prior}, &out)
			if out.State.Raw.IsNull() || !out.Diagnostics.HasError() {
				t.Fatalf("raw code %d removed ownership: %v", code, out.Diagnostics)
			}
		})
	}
}

func TestGatewayExplicitUuidImportUsesJournalProofAndNeverAdoptsCurrentForeignRoute(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	owned, foreign := "192.0.2.1", "192.0.2.99"
	p := &strictGatewayPanel{id: id, status: "SUCCESS", body: client.ChangeVmGatewayRequest{NetworkID: 42, Gateway: &owned}, gateway: &foreign}
	r := &VmGatewayResource{client: strictGatewayClient(t, p)}
	_, empty := gatewayRaw(t, nil)
	imported := resource.ImportStateResponse{State: empty}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "7:42:" + id}, &imported)
	if imported.Diagnostics.HasError() {
		t.Fatal(imported.Diagnostics)
	}
	observed := resource.ReadResponse{State: imported.State}
	r.Read(context.Background(), resource.ReadRequest{State: imported.State}, &observed)
	var state VmGatewayResourceModel
	observed.State.Get(context.Background(), &state)
	if observed.Diagnostics.HasError() || state.OwnedGateway.ValueString() != owned || !state.Gateway.IsNull() || state.PlatformOperationUUID.ValueString() != id || p.stats().physical != 0 || p.stats().reserves != 0 || p.stats().submits != 0 {
		t.Fatalf("state=%+v counters=%+v diag=%v", state, p.stats(), observed.Diagnostics)
	}
}

func TestGatewayPreclaimBusyResumesTheSameUuidWithoutASecondPhysicalWrite(t *testing.T) {
	p := &strictGatewayPanel{submitBusy: 1}
	r := &VmGatewayResource{client: strictGatewayClient(t, p), poll: time.Millisecond}
	plan, empty := gatewayRaw(t, nil)
	out := resource.CreateResponse{State: empty}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &out)
	if out.Diagnostics.HasError() || p.stats().physical != 1 || p.stats().submits != 2 || p.stats().reserves != 1 {
		t.Fatalf("counters=%+v diag=%v", p.stats(), out.Diagnostics)
	}
	var state VmGatewayResourceModel
	out.State.Get(context.Background(), &state)
	if state.PlatformOperationUUID.ValueString() != p.id || state.PlatformOperationStatus.ValueString() != "SUCCESS" {
		t.Fatal(state)
	}
}
