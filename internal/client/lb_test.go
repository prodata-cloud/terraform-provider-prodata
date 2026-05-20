package client

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A realistic V1 success envelope for an internal, FRONTEND-source load balancer
// (modeled on a captured panel-main getLoadBalancer response).
const lbGetSuccessBody = `{"error":0,"errMessage":null,"data":{` +
	`"id":236,"name":"lb1b65-probe","description":"probe lb","isPublic":false,` +
	`"status":{"id":0,"name":"NEW","description":"Новый"},"userNet":115107,` +
	`"createdUser":172,"dateCreated":"2026-05-19T07:33:49.632382Z",` +
	`"localUserVip":"10.0.0.137","provisioningPublicIp":null,"ip":"10.0.0.137",` +
	`"protocol":"tcp","extraIps":["10.0.0.138","10.0.0.139"],` +
	`"backends":[{"id":414,"ip":"10.0.0.130","status":{"id":0,"name":"NEW"},` +
	`"userVM":{"guid":"ECOUJI1777372337081"}}],` +
	`"projectId":null,"projectIds":null,` +
	`"ports":[{"id":157,"srcPort":80,"dstPort":8080,"loadBalancerId":236}],` +
	`"source":"FRONTEND","region":"UZ5","projectTag":"default-project-89"}}`

func TestCreateLoadBalancerFrontend_Success(t *testing.T) {
	body := `{"error":0,"errMessage":null,"data":{"id":300,"name":"web-lb","isPublic":true,` +
		`"status":{"name":"NEW"},"userNet":500,"provisioningPublicIp":"203.0.113.5",` +
		`"localUserVip":"10.0.0.10","protocol":"TCP","source":"FRONTEND",` +
		`"ports":[{"srcPort":443,"dstPort":8443}],"backends":[]}}`
	server, cap := newCapturingServer(t, 200, body)
	defer server.Close()

	c := newTestClient(t, server)
	lb, err := c.CreateLoadBalancerFrontend(context.Background(), LoadBalancerRequest{
		Name:      "web-lb",
		IsPublic:  true,
		Protocol:  "TCP",
		UserNetID: 500,
		Backends:  []LbBackendRef{{UserVmID: "GUID123"}},
		Ports:     []LbPortReq{{BalancerPort: 443, BackendPort: 8443}},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cap.method != "POST" {
		t.Errorf("method = %q, want POST", cap.method)
	}
	if cap.path != "/panel-main/api/loadbalancer/createLoadbalancer" {
		t.Errorf("path = %q, want /panel-main/api/loadbalancer/createLoadbalancer", cap.path)
	}
	// ADR-4 / PR1b A3: the provider never pins loadBalancerVip or extraIps —
	// panel-main auto-allocates them.
	if _, ok := cap.body["loadBalancerVip"]; ok {
		t.Error("request body must not contain loadBalancerVip")
	}
	if _, ok := cap.body["extraIps"]; ok {
		t.Error("request body must not contain extraIps")
	}
	if _, ok := cap.body["userNetId"]; !ok {
		t.Error("request body must contain userNetId")
	}

	if lb.ID != 300 {
		t.Errorf("lb.ID = %d, want 300", lb.ID)
	}
	if lb.Type != LbTypeExternal {
		t.Errorf("lb.Type = %q, want %q (isPublic true)", lb.Type, LbTypeExternal)
	}
	if lb.PublicIP != "203.0.113.5" {
		t.Errorf("lb.PublicIP = %q, want 203.0.113.5", lb.PublicIP)
	}
}

func TestCreateLoadBalancerCCM_Success(t *testing.T) {
	body := `{"error":0,"errMessage":null,"data":{"id":301,"name":"k8s-lb","isPublic":false,` +
		`"status":{"name":"NEW"},"source":"CCM","protocol":"TCP"}}`
	server, cap := newCapturingServer(t, 200, body)
	defer server.Close()

	c := newTestClient(t, server)
	lb, err := c.CreateLoadBalancerCCM(context.Background(), CreateLoadBalancerCCMRequest{
		Name:       "k8s-lb",
		NodePoolID: 88,
		IsPublic:   false,
		Protocol:   "TCP",
		Ports:      []LbPortReq{{BalancerPort: 80, BackendPort: 30080}},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cap.path != "/panel-main/api/ccm/loadbalancer/create" {
		t.Errorf("path = %q, want /panel-main/api/ccm/loadbalancer/create", cap.path)
	}
	if got := cap.body["nodePoolId"]; got != float64(88) {
		t.Errorf("request nodePoolId = %v, want 88", got)
	}
	if lb.Source != LbSourceCCM {
		t.Errorf("lb.Source = %q, want %q", lb.Source, LbSourceCCM)
	}
}

func TestGetLoadBalancer_Success(t *testing.T) {
	server, cap := newCapturingServer(t, 200, lbGetSuccessBody)
	defer server.Close()

	c := newTestClient(t, server)
	lb, err := c.GetLoadBalancer(context.Background(), 236, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cap.method != "GET" {
		t.Errorf("method = %q, want GET", cap.method)
	}
	if !strings.Contains(cap.rawQuery, "loadBalancerId=236") {
		t.Errorf("rawQuery = %q, want loadBalancerId=236", cap.rawQuery)
	}

	if lb.ID != 236 || lb.Name != "lb1b65-probe" {
		t.Errorf("lb = %+v, want id 236 name lb1b65-probe", lb)
	}
	if lb.Type != LbTypeInternal {
		t.Errorf("lb.Type = %q, want %q (isPublic false)", lb.Type, LbTypeInternal)
	}
	if lb.Source != LbSourceFrontend {
		t.Errorf("lb.Source = %q, want %q", lb.Source, LbSourceFrontend)
	}
	if lb.Status != LbStatusNew {
		t.Errorf("lb.Status = %q, want %q", lb.Status, LbStatusNew)
	}
	if lb.NetworkID != 115107 {
		t.Errorf("lb.NetworkID = %d, want 115107 (from userNet)", lb.NetworkID)
	}
	if lb.PublicIP != "" {
		t.Errorf("lb.PublicIP = %q, want empty (provisioningPublicIp null)", lb.PublicIP)
	}
	if lb.PrivateIP != "10.0.0.137" {
		t.Errorf("lb.PrivateIP = %q, want 10.0.0.137 (from localUserVip)", lb.PrivateIP)
	}
	if lb.Region != "UZ5" || lb.ProjectTag != "default-project-89" {
		t.Errorf("region/projectTag = %q/%q, want UZ5/default-project-89", lb.Region, lb.ProjectTag)
	}
	if len(lb.Backends) != 1 {
		t.Fatalf("len(backends) = %d, want 1", len(lb.Backends))
	}
	if lb.Backends[0].Guid != "ECOUJI1777372337081" {
		t.Errorf("backend guid = %q, want ECOUJI1777372337081", lb.Backends[0].Guid)
	}
	if lb.Backends[0].IP != "10.0.0.130" || lb.Backends[0].Status != LbStatusNew {
		t.Errorf("backend = %+v, want ip 10.0.0.130 status NEW", lb.Backends[0])
	}
	if len(lb.Ports) != 1 {
		t.Fatalf("len(ports) = %d, want 1", len(lb.Ports))
	}
	if lb.Ports[0].Port != 80 || lb.Ports[0].TargetPort != 8080 {
		t.Errorf("port = %+v, want 80->8080", lb.Ports[0])
	}
	// Regression: the captured fixture has protocol:"tcp" (lowercase, as panel
	// stores it on legacy LBs); the adapter must upper-case it so state stays
	// stable against the schema's OneOf("TCP","UDP") validator.
	if lb.Protocol != LbProtocolTCP {
		t.Errorf("lb.Protocol = %q, want %q (upper-cased from fixture)", lb.Protocol, LbProtocolTCP)
	}
}

func TestGetLoadBalancer_ProtocolUDPLowercase(t *testing.T) {
	body := `{"error":0,"errMessage":null,"data":{"id":1,"name":"udp-lb","isPublic":false,` +
		`"status":{"name":"SUCCESS"},"source":"FRONTEND","protocol":"udp"}}`
	server := newTestServer(200, body)
	defer server.Close()

	c := newTestClient(t, server)
	lb, err := c.GetLoadBalancer(context.Background(), 1, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lb.Protocol != LbProtocolUDP {
		t.Errorf("lb.Protocol = %q, want %q (upper-cased from lowercase fixture)", lb.Protocol, LbProtocolUDP)
	}
}

func TestGetLoadBalancer_NotFound736(t *testing.T) {
	body := `{"success":false,"data":null,"errors":[{"code":736,"message":"Load balancer not found."}]}`
	server := newTestServer(404, body)
	defer server.Close()

	c := newTestClient(t, server)
	_, err := c.GetLoadBalancer(context.Background(), 999999, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsNotFound(err) {
		t.Errorf("expected IsNotFound=true, got: %v", err)
	}
}

func TestListLoadBalancers_Success(t *testing.T) {
	body := `{"error":0,"errMessage":null,"data":[` +
		`{"id":1,"name":"a","isPublic":true,"status":{"name":"SUCCESS"},"source":"FRONTEND"},` +
		`{"id":2,"name":"b","isPublic":false,"status":{"name":"NEW"},"source":"CCM"}]}`
	server := newTestServer(200, body)
	defer server.Close()

	c := newTestClient(t, server)
	lbs, err := c.ListLoadBalancers(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lbs) != 2 {
		t.Fatalf("len = %d, want 2", len(lbs))
	}
	if lbs[0].ID != 1 || lbs[0].Type != LbTypeExternal {
		t.Errorf("lbs[0] = %+v, want id 1 type external", lbs[0])
	}
	if lbs[1].ID != 2 || lbs[1].Source != LbSourceCCM {
		t.Errorf("lbs[1] = %+v, want id 2 source CCM", lbs[1])
	}
}

func TestListLoadBalancers_Empty(t *testing.T) {
	server := newTestServer(200, `{"error":0,"errMessage":null,"data":[]}`)
	defer server.Close()

	c := newTestClient(t, server)
	lbs, err := c.ListLoadBalancers(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lbs) != 0 {
		t.Errorf("len = %d, want 0", len(lbs))
	}
}

func TestConfigureLoadBalancerFrontend_Success(t *testing.T) {
	server, cap := newCapturingServer(t, 200, lbGetSuccessBody)
	defer server.Close()

	c := newTestClient(t, server)
	_, err := c.ConfigureLoadBalancerFrontend(context.Background(), 55, LoadBalancerRequest{
		Name:      "renamed",
		Protocol:  "TCP",
		UserNetID: 115107,
		Backends:  []LbBackendRef{{UserVmID: "GUID"}},
		Ports:     []LbPortReq{{BalancerPort: 80, BackendPort: 8080}},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cap.method != "POST" {
		t.Errorf("method = %q, want POST", cap.method)
	}
	if cap.path != "/panel-main/api/loadbalancer/configureLoadbalancer/55" {
		t.Errorf("path = %q, want .../api/loadbalancer/configureLoadbalancer/55", cap.path)
	}
}

func TestConfigureLoadBalancerCCM_Success(t *testing.T) {
	server, cap := newCapturingServer(t, 200, lbGetSuccessBody)
	defer server.Close()

	c := newTestClient(t, server)
	_, err := c.ConfigureLoadBalancerCCM(context.Background(), 55, LoadBalancerRequest{
		Name:      "renamed",
		Protocol:  "TCP",
		UserNetID: 115107,
		Ports:     []LbPortReq{{BalancerPort: 80, BackendPort: 8080}},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cap.path != "/panel-main/api/ccm/loadbalancer/configureLoadbalancer/55" {
		t.Errorf("path = %q, want .../api/ccm/loadbalancer/configureLoadbalancer/55", cap.path)
	}
}

func TestDeleteLoadBalancerFrontend_Success(t *testing.T) {
	server, cap := newCapturingServer(t, 200, `{"error":0,"errMessage":null,"data":{}}`)
	defer server.Close()

	c := newTestClient(t, server)
	if err := c.DeleteLoadBalancerFrontend(context.Background(), 77, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cap.method != "POST" {
		t.Errorf("method = %q, want POST", cap.method)
	}
	if cap.path != "/panel-main/api/loadbalancer/deleteLoadbalancer/77" {
		t.Errorf("path = %q, want .../api/loadbalancer/deleteLoadbalancer/77", cap.path)
	}
}

func TestDeleteLoadBalancerCCM_Success(t *testing.T) {
	server, cap := newCapturingServer(t, 200, `{"error":0,"errMessage":null,"data":{}}`)
	defer server.Close()

	c := newTestClient(t, server)
	if err := c.DeleteLoadBalancerCCM(context.Background(), 77, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// verified-fact-8: the explicit long-form CCM delete path.
	if cap.path != "/panel-main/api/ccm/loadbalancer/deleteLoadbalancer/77" {
		t.Errorf("path = %q, want .../api/ccm/loadbalancer/deleteLoadbalancer/77", cap.path)
	}
}

// Round-2 finding 5.3 — deleting a CCM-source LB via the Frontend endpoint
// returns {"error":500,...}; the client must surface it, not mask it as 736.
func TestDeleteLoadBalancerFrontend_CrossSourceRejected(t *testing.T) {
	body := `{"error":500,"errMessage":"Cannot do this operation with this resource","data":null}`
	server := newTestServer(200, body)
	defer server.Close()

	c := newTestClient(t, server)
	err := c.DeleteLoadBalancerFrontend(context.Background(), 42, nil)
	if err == nil {
		t.Fatal("expected error for cross-source delete")
	}
	if !IsAPIError(err, 500) {
		t.Errorf("expected code 500, got: %v", err)
	}
	if IsNotFound(err) {
		t.Error("cross-source 500 must NOT be treated as not-found")
	}
}

func TestCreateLoadBalancerFrontend_ServerError(t *testing.T) {
	body := `{"success":false,"data":null,"errors":[{"code":627,"message":"Unhandled error"}]}`
	server := newTestServer(500, body)
	defer server.Close()

	c := newTestClient(t, server)
	_, err := c.CreateLoadBalancerFrontend(context.Background(), LoadBalancerRequest{
		Name: "x", Protocol: "TCP", UserNetID: 1,
		Backends: []LbBackendRef{{UserVmID: "G"}},
		Ports:    []LbPortReq{{BalancerPort: 80, BackendPort: 80}},
	}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsAPIError(err, 627) {
		t.Errorf("expected code 627, got: %v", err)
	}
}

func TestConfigureLoadBalancerFrontend_DuplicateName701(t *testing.T) {
	body := `{"success":false,"data":null,"errors":[{"code":701,"message":"Load balancer name already exists"}]}`
	server := newTestServer(400, body)
	defer server.Close()

	c := newTestClient(t, server)
	_, err := c.ConfigureLoadBalancerFrontend(context.Background(), 5, LoadBalancerRequest{
		Name: "taken", Protocol: "TCP", UserNetID: 1,
		Backends: []LbBackendRef{{UserVmID: "G"}},
		Ports:    []LbPortReq{{BalancerPort: 80, BackendPort: 80}},
	}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsAPIError(err, 701) {
		t.Errorf("expected code 701, got: %v", err)
	}
}

// ---- live test, gated behind PRODATA_LIVE_TEST=1; if hitting a prod-kz host,
// also requires PRODATA_ALLOW_PROD_KZ_MUTATION + a tf-iac-33821- name prefix ----

const liveLbPrefix = "tf-iac-33821-"

func requireLbProdKzMutationAllowed(t *testing.T, baseURL, lbName string) {
	t.Helper()
	if !strings.Contains(baseURL, "kz") {
		return
	}
	if os.Getenv("PRODATA_ALLOW_PROD_KZ_MUTATION") != "tf-iac-33821" {
		t.Skip("prod-kz target detected — set PRODATA_ALLOW_PROD_KZ_MUTATION=tf-iac-33821 to allow mutating tests")
	}
	if !strings.HasPrefix(lbName, liveLbPrefix) {
		t.Fatalf("prod-kz load balancer name %q must start with %q", lbName, liveLbPrefix)
	}
}

func TestLive_LoadBalancerCRUD(t *testing.T) {
	if os.Getenv("PRODATA_LIVE_TEST") != "1" {
		t.Skip("set PRODATA_LIVE_TEST=1 to run live API tests")
	}

	baseURL := os.Getenv("PRODATA_API_BASE_URL")
	apiKey := os.Getenv("PRODATA_API_KEY_ID")
	apiSecret := os.Getenv("PRODATA_API_SECRET_KEY")
	region := os.Getenv("PRODATA_REGION")
	projectTag := os.Getenv("PRODATA_PROJECT_TAG")
	netIDRaw := os.Getenv("PRODATA_LB_TEST_NET_ID")
	backendGuid := os.Getenv("PRODATA_LB_TEST_VM_GUID")
	if baseURL == "" || apiKey == "" || apiSecret == "" || region == "" ||
		projectTag == "" || netIDRaw == "" || backendGuid == "" {
		t.Skip("PRODATA_API_BASE_URL/API_KEY_ID/API_SECRET_KEY/REGION/PROJECT_TAG/LB_TEST_NET_ID/LB_TEST_VM_GUID must be set")
	}
	netID, err := strconv.ParseInt(netIDRaw, 10, 64)
	if err != nil {
		t.Fatalf("PRODATA_LB_TEST_NET_ID = %q: %v", netIDRaw, err)
	}

	lbName := liveLbPrefix + "live-crud-" + strings.ToLower(time.Now().UTC().Format("20060102-150405"))
	requireLbProdKzMutationAllowed(t, baseURL, lbName)

	c, err := New(Config{
		APIBaseURL:   baseURL,
		APIKeyID:     apiKey,
		APISecretKey: apiSecret,
		UserAgent:    "tf-provider-prodata/live-test",
		Region:       region,
		ProjectTag:   projectTag,
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	ctx := context.Background()
	lb, err := c.CreateLoadBalancerFrontend(ctx, LoadBalancerRequest{
		Name:      lbName,
		IsPublic:  false,
		Protocol:  "TCP",
		UserNetID: netID,
		Backends:  []LbBackendRef{{UserVmID: backendGuid}},
		Ports:     []LbPortReq{{BalancerPort: 80, BackendPort: 8080}},
	}, nil)
	if err != nil {
		t.Fatalf("CreateLoadBalancerFrontend: %v", err)
	}
	t.Cleanup(func() {
		if err := c.DeleteLoadBalancerFrontend(context.Background(), lb.ID, nil); err != nil && !IsNotFound(err) {
			t.Logf("cleanup DeleteLoadBalancerFrontend(%d): %v", lb.ID, err)
		}
	})

	got, err := c.GetLoadBalancer(ctx, lb.ID, nil)
	if err != nil {
		t.Fatalf("GetLoadBalancer(%d): %v", lb.ID, err)
	}
	if got.Name != lbName {
		t.Errorf("GetLoadBalancer name = %q, want %q", got.Name, lbName)
	}
	if got.Source != LbSourceFrontend {
		t.Errorf("Source = %q, want %q", got.Source, LbSourceFrontend)
	}
}
