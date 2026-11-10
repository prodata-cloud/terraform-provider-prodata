package resources

import (
	"context"
	"encoding/json"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"net/http"
	"net/http/httptest"
	"strings"
	"terraform-provider-prodata/internal/client"
	"testing"
	"time"
)

func creationOperationWire(id, status string, cluster any) map[string]any {
	return map[string]any{"contract": client.ClusterCreationContract, "requestUuid": id, "clusterId": cluster, "status": status, "exists": status != "NOT_SUBMITTED", "canRelease": status == "NOT_SUBMITTED", "ready": status == "SUCCESS" || status == "NOT_SUBMITTED", "epoch": 91, "fingerprint": strings.Repeat("a", 64), "name": "tfc"}
}
func creationCapabilitiesWire() map[string]any {
	return map[string]any{"contract": client.ClusterCreationContract, "callerUuid": true, "reserveBeforeSubmit": true, "recoverableSubmission": true, "resolveTombstone": true, "compensatingDeleteFence": true, "journalIdentityFence": true}
}
func creationResourceFixture(t *testing.T, url string) (*K8sClusterResource, resource.CreateRequest, *resource.CreateResponse) {
	t.Helper()
	c, err := client.New(client.Config{APIBaseURL: url, APIKeyID: "test", APISecretKey: "test", Region: "TEST", ProjectTag: "test"})
	if err != nil {
		t.Fatal(err)
	}
	_, sch := k8sObjType(t)
	plan := k8sRaw(t, map[string]tftypes.Value{"name": nirStr("tfc"), "kubernetes_version": nirStr("v1.31.4"), "high_availability": credBool(false), "network_id": nirNum(7), "pod_cidr": nirStr("10.244.0.0/16"), "master_flavor_id": nirNum(7), "ssh_access_enabled": credBool(false), "public_endpoint_enabled": credBool(false)})
	return &K8sClusterResource{c: c, creationPoll: time.Millisecond}, resource.CreateRequest{Plan: tfsdk.Plan{Schema: sch, Raw: plan}}, &resource.CreateResponse{State: tfsdk.State{Schema: sch, Raw: tftypes.NewValue(plan.Type(), nil)}}
}
func TestClusterCreationCheckpointLostAcksAndKnownPreclaimRetry(t *testing.T) {
	var output *resource.CreateResponse
	known, status := "", ""
	reserves, submits, physical := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		answer := func(v any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": v})
		}
		v1 := func(v any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": v})
		}
		var checkpoint K8sClusterModel
		if d := output.State.Get(context.Background(), &checkpoint); d.HasError() {
			t.Error(d)
		}
		if checkpoint.RequestUUID.ValueString() == "" || checkpoint.Status.IsUnknown() || checkpoint.KubeConfig.IsUnknown() {
			t.Error("missing fully-known pre-HTTP checkpoint")
		}
		prefix := "/panel-main/api/v2/kubernetes/cluster-operations/"
		if strings.HasPrefix(q.URL.Path, prefix) {
			tail := strings.TrimPrefix(q.URL.Path, prefix)
			if tail == "capabilities" {
				if !checkpoint.ID.IsNull() {
					t.Error("canonical ID before reservation")
				}
				answer(creationCapabilitiesWire())
				return
			}
			id := strings.TrimSuffix(strings.TrimSuffix(tail, "/reservations"), "/submit")
			if known != "" && known != id {
				t.Error("creation UUID changed")
			}
			known = id
			if q.Method == "POST" && strings.HasSuffix(tail, "/reservations") {
				reserves++
				status = "RESERVED"
				lostVolumeAck(w)
				return
			}
			if q.Method == "POST" && strings.HasSuffix(tail, "/submit") {
				if checkpoint.ID.ValueInt64() != 313 {
					t.Error("canonical PK was not checkpointed before submit")
				}
				submits++
				if submits == 1 {
					status = "UNVERIFIED"
					answer(creationOperationWire(id, status, 313))
					return
				}
				physical++
				status = "SUCCESS"
				lostVolumeAck(w)
				return
			}
			if q.Method == "GET" {
				answer(creationOperationWire(id, status, 313))
				return
			}
			t.Errorf("unexpected lifecycle %s %s", q.Method, q.URL.Path)
			return
		}
		switch q.URL.Path {
		case "/panel-main/api/kubernetes/getMasterNodeConfig/false":
			v1([]map[string]any{{"id": 7, "cpu": 2, "ram": 4, "ssd": 20}})
		case "/panel-main/api/kubernetes/getClusters":
			v1([]any{})
		case "/panel-main/api/kubernetes/getCluster/313":
			v1(credClusterBody(credClusterView{status: "SUCCESS", version: "v1.31.4", secret: credB64(credKubeconfigYAML())}))
		default:
			t.Errorf("legacy or unrelated writer: %s %s", q.Method, q.URL.Path)
			http.NotFound(w, q)
		}
	}))
	defer server.Close()
	r, req, resp := creationResourceFixture(t, server.URL)
	output = resp
	r.Create(context.Background(), req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	got := credStateModel(t, resp.State)
	if got.ID.ValueInt64() != 313 || got.RequestUUID.ValueString() != known || reserves != 1 || submits != 2 || physical != 1 {
		t.Fatalf("model=%+v reserve/submit/physical=%d/%d/%d", got, reserves, submits, physical)
	}
}
func TestClusterCreationReadNeverActivatesOrDropsUnknownIdentity(t *testing.T) {
	for _, mode := range []string{"raw404", "pending", "negative", "historicalDeleted"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				calls++
				if q.Method != "GET" {
					t.Error("Read mutated")
				}
				if mode == "raw404" {
					http.NotFound(w, q)
					return
				}
				status := "UNVERIFIED"
				var cluster any = 313
				if mode == "negative" {
					status = "NOT_SUBMITTED"
					cluster = nil
				}
				if mode == "historicalDeleted" {
					status = "SUCCESS"
				}
				view := creationOperationWire(guestUUID, status, cluster)
				if mode == "historicalDeleted" {
					view["exists"] = false
				}
				protectionReply(w, view)
			}))
			defer server.Close()
			r, req, _ := creationResourceFixture(t, server.URL)
			state := tfsdk.State(req.Plan)
			for key, value := range map[string]any{"id": types.Int64Null(), "request_uuid": guestUUID, "region": "TEST", "project_tag": "test", "status": "RESERVING"} {
				if d := state.SetAttribute(context.Background(), path.Root(key), value); d.HasError() {
					t.Fatal(d)
				}
			}
			resp := resource.ReadResponse{State: state}
			r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
			gone := mode == "negative" || mode == "historicalDeleted"
			if resp.Diagnostics.HasError() || resp.State.Raw.IsNull() != gone || calls != 1 {
				t.Fatalf("mode=%s calls=%d diag=%v", mode, calls, resp.Diagnostics)
			}
		})
	}
}
func TestClusterCreationNullableIdentityNeedsPositiveCancellation(t *testing.T) {
	for _, positive := range []bool{false, true} {
		t.Run(map[bool]string{true: "positive", false: "unknown"}[positive], func(t *testing.T) {
			cancels := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				if q.Method != "DELETE" || !strings.HasSuffix(q.URL.Path, "/cluster-operations/"+guestUUID) {
					t.Errorf("unsafe nullable cleanup: %s %s", q.Method, q.URL.Path)
				}
				cancels++
				if !positive {
					w.WriteHeader(503)
					return
				}
				protectionReply(w, creationOperationWire(guestUUID, "NOT_SUBMITTED", nil))
			}))
			defer server.Close()
			r, req, _ := creationResourceFixture(t, server.URL)
			state := tfsdk.State(req.Plan)
			for key, value := range map[string]any{"id": types.Int64Null(), "request_uuid": guestUUID, "region": "TEST", "status": "RESERVING"} {
				if d := state.SetAttribute(context.Background(), path.Root(key), value); d.HasError() {
					t.Fatal(d)
				}
			}
			out := resource.DeleteResponse{State: state}
			r.Delete(context.Background(), resource.DeleteRequest{State: state}, &out)
			if out.Diagnostics.HasError() == positive || cancels != 1 {
				t.Fatalf("positive=%t cancels=%d diag=%v", positive, cancels, out.Diagnostics)
			}
		})
	}
}

func TestClusterCreationHistoricalSuccessCannotEraseNewerPendingPhase(t *testing.T) {
	c := protectionClient(t, func(w http.ResponseWriter, q *http.Request) {
		if strings.Contains(q.URL.Path, "/cluster-operations/") {
			protectionReply(w, creationOperationWire(guestUUID, "SUCCESS", 313))
			return
		}
		w.WriteHeader(503)
	})
	r := &K8sClusterResource{c: c}
	state := clusterDeletionState(t)
	for key, value := range map[string]any{"name": "tfc", "request_uuid": guestUUID, "status": "PROCESSING", "blocked": true} {
		if d := state.SetAttribute(context.Background(), path.Root(key), value); d.HasError() {
			t.Fatal(d)
		}
	}
	out := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &out)
	got := credStateModel(t, out.State)
	if !out.Diagnostics.HasError() || got.Status.ValueString() != "PROCESSING" || !got.Blocked.ValueBool() || got.ID.ValueInt64() != 313 {
		t.Fatalf("historical creation overwrote newer state: %+v diag=%v", got, out.Diagnostics)
	}
}

func TestClusterCreationRequiresRecoveryFencesBeforeReservation(t *testing.T) {
	for _, missing := range []string{"compensatingDeleteFence", "journalIdentityFence"} {
		t.Run(missing, func(t *testing.T) {
			capabilities, mutations := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				if q.Method != http.MethodGet {
					mutations++
					t.Errorf("mutated before compensation capability: %s %s", q.Method, q.URL.Path)
					http.Error(w, "unexpected mutation", http.StatusServiceUnavailable)
					return
				}
				v1 := func(data any) {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": data})
				}
				switch q.URL.Path {
				case "/panel-main/api/kubernetes/getMasterNodeConfig/false":
					v1([]map[string]any{{"id": 7, "cpu": 2, "ram": 4, "ssd": 20}})
				case "/panel-main/api/kubernetes/getClusters":
					v1([]any{})
				case "/panel-main/api/v2/kubernetes/cluster-operations/capabilities":
					capabilities++
					caps := creationCapabilitiesWire()
					delete(caps, missing)
					protectionReply(w, caps)
				default:
					t.Errorf("unexpected read %s", q.URL.Path)
					http.NotFound(w, q)
				}
			}))
			defer server.Close()
			r, req, resp := creationResourceFixture(t, server.URL)
			r.Create(context.Background(), req, resp)
			if !resp.Diagnostics.HasError() || capabilities != 1 || mutations != 0 {
				t.Fatalf("diag=%v caps=%d writes=%d", resp.Diagnostics, capabilities, mutations)
			}
			// Only a read-only capability probe occurred, so there is no remote reservation to retain.
			if !resp.State.Raw.IsNull() {
				t.Fatal("capability refusal retained a fresh resource despite no remote hold")
			}

		})
	}
}
