package client

import (
	"context"
	"errors"
	"strings"
	"testing"
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
	server, capture := newCapturingServer(t, 200, body)
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

	if capture.method != "POST" {
		t.Errorf("method = %q, want POST", capture.method)
	}
	if capture.path != "/panel-main/api/loadbalancer/createLoadbalancer" {
		t.Errorf("path = %q, want /panel-main/api/loadbalancer/createLoadbalancer", capture.path)
	}
	// ADR-4 / PR1b A3: the provider never pins loadBalancerVip or extraIps —
	// panel-main auto-allocates them.
	if _, ok := capture.body["loadBalancerVip"]; ok {
		t.Error("request body must not contain loadBalancerVip")
	}
	if _, ok := capture.body["extraIps"]; ok {
		t.Error("request body must not contain extraIps")
	}
	if _, ok := capture.body["userNetId"]; !ok {
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
	server, capture := newCapturingServer(t, 200, body)
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

	if capture.path != "/panel-main/api/ccm/loadbalancer/create" {
		t.Errorf("path = %q, want /panel-main/api/ccm/loadbalancer/create", capture.path)
	}
	if got := capture.body["nodePoolId"]; got != float64(88) {
		t.Errorf("request nodePoolId = %v, want 88", got)
	}
	if lb.Source != LbSourceCCM {
		t.Errorf("lb.Source = %q, want %q", lb.Source, LbSourceCCM)
	}
}

func TestGetLoadBalancer_Success(t *testing.T) {
	server, capture := newCapturingServer(t, 200, lbGetSuccessBody)
	defer server.Close()

	c := newTestClient(t, server)
	lb, err := c.GetLoadBalancer(context.Background(), 236, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capture.method != "GET" {
		t.Errorf("method = %q, want GET", capture.method)
	}
	if !strings.Contains(capture.rawQuery, "loadBalancerId=236") {
		t.Errorf("rawQuery = %q, want loadBalancerId=236", capture.rawQuery)
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

func TestLBErrorDetail(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantSubst string
	}{
		{"701 duplicate name", &APIError{StatusCode: 400, Codes: []int{701}, Message: "x"}, "already exists"},
		{"736 not found", &APIError{StatusCode: 404, Codes: []int{736}, Message: "x"}, "not found"},
		{"737 free IPs", &APIError{StatusCode: 400, Codes: []int{737}, Message: "x"}, "free IPs"},
		{"743 no ip pool", &APIError{StatusCode: 503, Codes: []int{743}, Message: "x"}, "IP pool"},
		{"744 no compute capacity", &APIError{StatusCode: 503, Codes: []int{744}, Message: "x"}, "compute capacity"},
		{"747 provisioning failed", &APIError{StatusCode: 502, Codes: []int{747}, Message: "x"}, "could not be provisioned"},
		{"627 unhandled falls through to raw", &APIError{StatusCode: 500, Codes: []int{627}, Message: "Unhandled error"}, "627"},
		{"unknown code falls through", &APIError{StatusCode: 500, Codes: []int{999}, Message: "raw msg"}, "raw msg"},
		{"non-api error falls through", errors.New("plain"), "plain"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := LBErrorDetail(c.err)
			if !strings.Contains(got, c.wantSubst) {
				t.Errorf("LBErrorDetail() = %q, want substring %q", got, c.wantSubst)
			}
		})
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
	server, capture := newCapturingServer(t, 200, lbGetSuccessBody)
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
	if capture.method != "POST" {
		t.Errorf("method = %q, want POST", capture.method)
	}
	if capture.path != "/panel-main/api/loadbalancer/configureLoadbalancer/55" {
		t.Errorf("path = %q, want .../api/loadbalancer/configureLoadbalancer/55", capture.path)
	}
}

func TestConfigureLoadBalancerCCM_Success(t *testing.T) {
	server, capture := newCapturingServer(t, 200, lbGetSuccessBody)
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
	if capture.path != "/panel-main/api/ccm/loadbalancer/configureLoadbalancer/55" {
		t.Errorf("path = %q, want .../api/ccm/loadbalancer/configureLoadbalancer/55", capture.path)
	}
}

func TestDeleteLoadBalancerFrontend_Success(t *testing.T) {
	server, capture := newCapturingServer(t, 200, `{"error":0,"errMessage":null,"data":{}}`)
	defer server.Close()

	c := newTestClient(t, server)
	if err := c.DeleteLoadBalancerFrontend(context.Background(), 77, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capture.method != "POST" {
		t.Errorf("method = %q, want POST", capture.method)
	}
	if capture.path != "/panel-main/api/loadbalancer/deleteLoadbalancer/77" {
		t.Errorf("path = %q, want .../api/loadbalancer/deleteLoadbalancer/77", capture.path)
	}
}

func TestDeleteLoadBalancerCCM_Success(t *testing.T) {
	server, capture := newCapturingServer(t, 200, `{"error":0,"errMessage":null,"data":{}}`)
	defer server.Close()

	c := newTestClient(t, server)
	if err := c.DeleteLoadBalancerCCM(context.Background(), 77, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// verified-fact-8: the explicit long-form CCM delete path.
	if capture.path != "/panel-main/api/ccm/loadbalancer/deleteLoadbalancer/77" {
		t.Errorf("path = %q, want .../api/ccm/loadbalancer/deleteLoadbalancer/77", capture.path)
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

// Full create/read/update/delete behavior against a live API is covered by the
// TF_ACC acceptance suite in the provider package (TestAccLb_basic), which also
// owns the production-mutation guard and disposable name prefix.
