package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A realistic V1 success envelope for getCluster, modeled on panel-main's
// ClusterDTO wire shape: status is a nested object (read .name), the master
// config is the flat MasterNodeConfigDTO (post-G13), and the kubeconfig is under
// "clusterConfigSecret". These keys are pinned here because a wrong key would
// silently decode to a zero value.
const clusterGetSuccessBody = `{"error":0,"errMessage":null,"data":{` +
	`"id":313,"status":{"id":1,"name":"SUCCESS","description":"Успешно"},` +
	`"kuberVersion":"v1.31.4","name":"tf-cluster","apiEndpoint":"https://10.0.0.5:6443",` +
	`"isPublic":false,"sshKeyEncoded":"c3NoLWtleQ==","privateKeyEncoded":"cHJpdmF0ZQ==",` +
	`"clusterConfigSecret":"a3ViZWNvbmZpZw==","isHa":true,` +
	`"masterNodeCount":3,"workerNodeCount":2,"nodePoolCount":1,` +
	`"userNetDTO":{"id":115107,"ip":"10.0.0.0","mask":"24"},` +
	`"masterNodeConfiguration":{"id":7,"cpu":4,"ram":8,"ssd":40,"isHa":true,` +
	`"regionId":6,"operationTypeId":44,"resourceId":30},` +
	`"podSubnet":"10.244.0.0/16","blocked":false,"ipAddressesCount":5,` +
	`"projectId":89,"projectIds":null,"currentProjectId":null,` +
	`"dateCreated":"2026-06-15T08:30:00Z","description":"tf managed"}}`

func TestCreateCluster_SendsExactFieldNames(t *testing.T) {
	body := `{"error":0,"errMessage":null,"data":{"id":500,"status":{"name":"NEW"},` +
		`"name":"tf-cluster","kuberVersion":"v1.31.4","isHa":false,` +
		`"masterNodeConfiguration":{"id":7,"cpu":2,"ram":4,"ssd":20,"isHa":false}}}`
	server, capture := newCapturingServer(t, 200, body)
	defer server.Close()

	c := newTestClient(t, server)
	cluster, err := c.CreateCluster(context.Background(), CreateClusterRequest{
		ClusterName:        "tf-cluster",
		WorkerDiskSize:     40,
		WorkerCPU:          2,
		WorkerRAM:          4,
		WorkerReplicas:     2,
		Addresses:          []string{"10.0.0.10-10.0.0.20"},
		KuberVersion:       "v1.31.4",
		NodePoolName:       "default",
		PodSubnet:          "10.244.0.0/16",
		LocalNetID:         115107,
		MasterNodeConfigID: 7,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capture.method != "POST" {
		t.Errorf("method = %q, want POST", capture.method)
	}
	if capture.path != "/panel-main/api/kubernetes/createCluster" {
		t.Errorf("path = %q, want /panel-main/api/kubernetes/createCluster", capture.path)
	}
	// Pin the make-or-break wire keys.
	for _, key := range []string{"clusterName", "workerCpu", "workerRam", "workerDiskSize",
		"workerReplicas", "addresses", "kuberVersion", "nodePoolName", "podSubnet",
		"localNetId", "masterNodeConfigId"} {
		if _, ok := capture.body[key]; !ok {
			t.Errorf("request body missing key %q; got keys %v", key, keysOf(capture.body))
		}
	}
	if got := capture.body["masterNodeConfigId"]; got != float64(7) {
		t.Errorf("masterNodeConfigId = %v, want 7", got)
	}
	// Dead fields must never be sent.
	if _, ok := capture.body["gateway"]; ok {
		t.Error("request body must not contain dead field gateway")
	}
	if _, ok := capture.body["prefix"]; ok {
		t.Error("request body must not contain dead field prefix")
	}
	// nodeSubnet is no longer sent: the backend derives the node subnet from the
	// local network's mask, so the provider must not carry it on the wire.
	if _, ok := capture.body["nodeSubnet"]; ok {
		t.Error("request body must not contain removed field nodeSubnet")
	}

	if cluster.ID != 500 {
		t.Errorf("cluster.ID = %d, want 500", cluster.ID)
	}
	if cluster.Status != ClusterStatusNew {
		t.Errorf("cluster.Status = %q, want NEW", cluster.Status)
	}
	// Counts are absent on the create response and must decode to zero, not error.
	if cluster.MasterNodeCount != 0 || cluster.NodePoolCount != 0 {
		t.Errorf("counts = %d/%d, want 0/0 on create", cluster.MasterNodeCount, cluster.NodePoolCount)
	}
	if cluster.MasterNodeConfig == nil || cluster.MasterNodeConfig.ID != 7 {
		t.Errorf("masterNodeConfig = %+v, want id 7", cluster.MasterNodeConfig)
	}
}

func TestKuberClient_SendsXLangEnglish(t *testing.T) {
	var gotLang string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLang = r.Header.Get("X-Lang")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"error":0,"errMessage":null,"data":[]}`))
	}))
	defer server.Close()

	c := newTestClient(t, server)
	if _, err := c.ListClusters(context.Background(), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotLang != "en" {
		t.Errorf("X-Lang = %q, want en (ADR-K1)", gotLang)
	}
}

func TestGetCluster_DecodesAllFields(t *testing.T) {
	server, capture := newCapturingServer(t, 200, clusterGetSuccessBody)
	defer server.Close()

	c := newTestClient(t, server)
	cl, err := c.GetCluster(context.Background(), 313, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capture.method != "GET" {
		t.Errorf("method = %q, want GET", capture.method)
	}
	if capture.path != "/panel-main/api/kubernetes/getCluster/313" {
		t.Errorf("path = %q, want .../getCluster/313", capture.path)
	}

	if cl.ID != 313 || cl.Name != "tf-cluster" {
		t.Errorf("cl = %+v, want id 313 name tf-cluster", cl)
	}
	if cl.Status != ClusterStatusSuccess {
		t.Errorf("Status = %q, want SUCCESS (from status.name)", cl.Status)
	}
	if cl.KubeVersion != "v1.31.4" {
		t.Errorf("KubeVersion = %q, want v1.31.4 (from kuberVersion)", cl.KubeVersion)
	}
	if cl.APIEndpoint != "https://10.0.0.5:6443" {
		t.Errorf("APIEndpoint = %q", cl.APIEndpoint)
	}
	if cl.Kubeconfig != "a3ViZWNvbmZpZw==" {
		t.Errorf("Kubeconfig = %q, want from clusterConfigSecret", cl.Kubeconfig)
	}
	if cl.PrivateKeyEncoded != "cHJpdmF0ZQ==" || cl.SSHKeyEncoded != "c3NoLWtleQ==" {
		t.Errorf("ssh/private = %q/%q", cl.SSHKeyEncoded, cl.PrivateKeyEncoded)
	}
	if !cl.IsHA || cl.IsPublic {
		t.Errorf("isHa/isPublic = %v/%v, want true/false", cl.IsHA, cl.IsPublic)
	}
	if cl.MasterNodeCount != 3 || cl.WorkerNodeCount != 2 || cl.NodePoolCount != 1 {
		t.Errorf("counts = %d/%d/%d, want 3/2/1", cl.MasterNodeCount, cl.WorkerNodeCount, cl.NodePoolCount)
	}
	if cl.PodSubnet != "10.244.0.0/16" || cl.IPAddressesCount != 5 || cl.ProjectID != 89 {
		t.Errorf("podSubnet/ips/project = %q/%d/%d", cl.PodSubnet, cl.IPAddressesCount, cl.ProjectID)
	}
	if cl.DateCreated != "2026-06-15T08:30:00Z" || cl.Description != "tf managed" {
		t.Errorf("dateCreated/description = %q/%q", cl.DateCreated, cl.Description)
	}
	if cl.MasterNodeConfig == nil {
		t.Fatal("MasterNodeConfig is nil")
	}
	mc := cl.MasterNodeConfig
	if mc.ID != 7 || mc.CPU != 4 || mc.RAM != 8 || mc.SSD != 40 || !mc.IsHA ||
		mc.RegionID != 6 || mc.OperationTypeID != 44 || mc.ResourceID != 30 {
		t.Errorf("MasterNodeConfig = %+v, want {7 4 8 40 true 6 44 30}", mc)
	}
}

func TestGetCluster_NotFound(t *testing.T) {
	// Business not-found arrives as a V1 envelope at HTTP 500 with errMessage.
	body := `{"data":null,"errMessage":"Cluster not found!","error":500}`
	server := newTestServer(500, body)
	defer server.Close()

	c := newTestClient(t, server)
	_, err := c.GetCluster(context.Background(), 999999, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsKuberNotFound(err) {
		t.Errorf("expected IsKuberNotFound=true, got: %v", err)
	}
}

func TestGetCluster_DeletedIsNotNotFound(t *testing.T) {
	// A soft-deleted cluster reads back with status DELETED at HTTP 200 — the
	// caller detects it via Status, NOT via an error.
	body := `{"error":0,"errMessage":null,"data":{"id":159,"status":{"name":"DELETED"},"name":"old"}}`
	server := newTestServer(200, body)
	defer server.Close()

	c := newTestClient(t, server)
	cl, err := c.GetCluster(context.Background(), 159, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cl.Status != ClusterStatusDeleted {
		t.Errorf("Status = %q, want DELETED", cl.Status)
	}
}

func TestListClusters_Empty(t *testing.T) {
	server := newTestServer(200, `{"error":0,"errMessage":null,"data":[]}`)
	defer server.Close()

	c := newTestClient(t, server)
	cs, err := c.ListClusters(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cs) != 0 {
		t.Errorf("len = %d, want 0", len(cs))
	}
}

func TestUpdateClusterVersion_PathAndQuery(t *testing.T) {
	server, capture := newCapturingServer(t, 200,
		`{"error":0,"errMessage":null,"data":{"id":5,"status":{"name":"PROCESSING"},"kuberVersion":"v1.31.4"}}`)
	defer server.Close()

	c := newTestClient(t, server)
	cl, err := c.UpdateClusterVersion(context.Background(), 5, "v1.31.4", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capture.path != "/panel-main/api/kubernetes/updateClusterKuberVersion/5" {
		t.Errorf("path = %q, want .../updateClusterKuberVersion/5", capture.path)
	}
	if !strings.Contains(capture.rawQuery, "version=v1.31.4") {
		t.Errorf("rawQuery = %q, want version=v1.31.4", capture.rawQuery)
	}
	if cl.Status != ClusterStatusProcessing {
		t.Errorf("Status = %q, want PROCESSING", cl.Status)
	}
}

func TestUpdateMasterConfig_BodyRecordFields(t *testing.T) {
	server, capture := newCapturingServer(t, 200, `{"error":0,"errMessage":null,"data":null}`)
	defer server.Close()

	c := newTestClient(t, server)
	err := c.UpdateMasterConfig(context.Background(),
		UpdateMasterConfigRequest{ClusterID: 5, MasterNodeConfigID: 9}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capture.method != "PATCH" {
		t.Errorf("method = %q, want PATCH", capture.method)
	}
	if capture.path != "/panel-main/api/kubernetes/updateMasterNodeConfig" {
		t.Errorf("path = %q", capture.path)
	}
	if capture.body["clusterId"] != float64(5) || capture.body["masterNodeConfigId"] != float64(9) {
		t.Errorf("body = %v, want clusterId 5 masterNodeConfigId 9", capture.body)
	}
}

func TestCreateNodePool_Fields(t *testing.T) {
	server, capture := newCapturingServer(t, 200, `{"error":0,"errMessage":null,"data":null}`)
	defer server.Close()

	c := newTestClient(t, server)
	err := c.CreateNodePool(context.Background(), CreateNodePoolRequest{
		ClusterID:      313,
		NodePoolName:   "workers-2",
		WorkerReplicas: 3,
		WorkerCPU:      2,
		WorkerRAM:      4,
		WorkerDiskSize: 40,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capture.path != "/panel-main/api/kubernetes/createNewNodePool" {
		t.Errorf("path = %q", capture.path)
	}
	if capture.body["clusterId"] != float64(313) || capture.body["nodePoolName"] != "workers-2" {
		t.Errorf("body = %v", capture.body)
	}
	for _, key := range []string{"workerReplicas", "workerCpu", "workerRam", "workerDiskSize"} {
		if _, ok := capture.body[key]; !ok {
			t.Errorf("body missing %q", key)
		}
	}
}

func TestGetNodePool_DecodesFields(t *testing.T) {
	body := `{"error":0,"errMessage":null,"data":{"id":486,"poolName":"workers",` +
		`"nodeCount":3,"nodeSubnet":24,"cpu":2,"ram":4,"ssd":40,` +
		`"status":{"name":"SUCCESS"},"clusterId":313,` +
		`"autoscaleEnabled":true,"minNodes":2,"maxNodes":5}}`
	server := newTestServer(200, body)
	defer server.Close()

	c := newTestClient(t, server)
	np, err := c.GetNodePool(context.Background(), 486, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if np.ID != 486 || np.Name != "workers" || np.NodeCount != 3 || np.ClusterID != 313 {
		t.Errorf("np = %+v", np)
	}
	if np.Status != ClusterStatusSuccess {
		t.Errorf("Status = %q, want SUCCESS (from status.name)", np.Status)
	}
	if !np.AutoscaleEnabled || np.MinNodes != 2 || np.MaxNodes != 5 {
		t.Errorf("autoscale = %v %d/%d, want true 2/5", np.AutoscaleEnabled, np.MinNodes, np.MaxNodes)
	}
}

func TestDeleteNodePool_LastWorkerPool756(t *testing.T) {
	// G1 guard surfaces as the V2 envelope (ApiException) at HTTP 409.
	body := `{"success":false,"data":null,"errors":[{"code":756,` +
		`"message":"Cannot delete the last worker node pool of a cluster. Delete the cluster instead."}]}`
	server := newTestServer(409, body)
	defer server.Close()

	c := newTestClient(t, server)
	err := c.DeleteNodePool(context.Background(), 486, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsLastWorkerPool(err) {
		t.Errorf("expected IsLastWorkerPool=true, got: %v", err)
	}
	if !strings.Contains(KuberErrorDetail(err), "last worker node pool") {
		t.Errorf("KuberErrorDetail = %q", KuberErrorDetail(err))
	}
}

func TestChangeNodePoolSize_Body(t *testing.T) {
	server, capture := newCapturingServer(t, 200, `{"error":0,"errMessage":null,"data":null}`)
	defer server.Close()

	c := newTestClient(t, server)
	err := c.ChangeNodePoolSize(context.Background(),
		ModifyNodePoolRequest{ClusterID: 313, NodePoolID: 486, WorkerReplicas: 4}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capture.path != "/panel-main/api/kubernetes/changeNodePoolSize" {
		t.Errorf("path = %q", capture.path)
	}
	if capture.body["clusterId"] != float64(313) || capture.body["nodePoolId"] != float64(486) ||
		capture.body["workerReplicas"] != float64(4) {
		t.Errorf("body = %v", capture.body)
	}
}

func TestListKuberVersions_DecodesIsDebug(t *testing.T) {
	body := `{"error":0,"errMessage":null,"data":[` +
		`{"id":1,"version":"v1.31.4","isDebug":false},` +
		`{"id":2,"version":"v1.99.0","isDebug":true}]}`
	server, capture := newCapturingServer(t, 200, body)
	defer server.Close()

	c := newTestClient(t, server)
	vs, err := c.ListKuberVersions(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capture.path != "/panel-main/api/kubernetes/getKuberVersions" {
		t.Errorf("path = %q", capture.path)
	}
	if len(vs) != 2 {
		t.Fatalf("len = %d, want 2", len(vs))
	}
	if vs[0].Version != "v1.31.4" || vs[0].IsDebug {
		t.Errorf("vs[0] = %+v", vs[0])
	}
	if !vs[1].IsDebug {
		t.Errorf("vs[1].IsDebug = false, want true")
	}
}

func TestGetMasterNodeConfigs_PathAndDecode(t *testing.T) {
	body := `{"error":0,"errMessage":null,"data":[` +
		`{"id":7,"cpu":4,"ram":8,"ssd":40,"isHa":true,"regionId":6,"operationTypeId":44,"resourceId":30}]}`
	server, capture := newCapturingServer(t, 200, body)
	defer server.Close()

	c := newTestClient(t, server)
	cfgs, err := c.GetMasterNodeConfigs(context.Background(), true, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capture.path != "/panel-main/api/kubernetes/getMasterNodeConfig/true" {
		t.Errorf("path = %q, want .../getMasterNodeConfig/true", capture.path)
	}
	if len(cfgs) != 1 {
		t.Fatalf("len = %d, want 1", len(cfgs))
	}
	if cfgs[0].ID != 7 || cfgs[0].CPU != 4 || cfgs[0].RAM != 8 || cfgs[0].SSD != 40 || !cfgs[0].IsHA {
		t.Errorf("cfg = %+v", cfgs[0])
	}
}

func TestKuberErrorDetail(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantSubst string
	}{
		{"756 last worker pool", &APIError{StatusCode: 409, Codes: []int{756}, Message: "x"}, "last worker node pool"},
		{"757 version unavailable", &APIError{StatusCode: 422, Codes: []int{757}, Message: "x"}, "not available in this region"},
		{"duplicate cluster name", &APIError{StatusCode: 500, Message: "Cluster with this name already exist"}, "already exists"},
		{"duplicate pool name", &APIError{StatusCode: 500, Message: "Node pool with this name already exists"}, "node pool with this name"},
		{"unknown falls through", &APIError{StatusCode: 500, Codes: []int{627}, Message: "raw"}, "raw"},
		{"non-api falls through", errors.New("plain"), "plain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := KuberErrorDetail(tc.err)
			if !strings.Contains(strings.ToLower(got), strings.ToLower(tc.wantSubst)) {
				t.Errorf("KuberErrorDetail() = %q, want substring %q", got, tc.wantSubst)
			}
		})
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestSizeClassByID_and_FlavorIDBySize(t *testing.T) {
	// Mirrors the real catalog: a clean 3-tier ladder per HA mode (cpu 2<8<16),
	// supplied out of order to prove ranking is by capacity, not input order.
	flavors := []MasterNodeConfig{
		{ID: 12, CPU: 8, RAM: 16, SSD: 50, IsHA: false},   // medium / basic
		{ID: 11, CPU: 2, RAM: 4, SSD: 20, IsHA: false},    // small / basic
		{ID: 13, CPU: 16, RAM: 32, SSD: 100, IsHA: false}, // large / basic
		{ID: 21, CPU: 2, RAM: 4, SSD: 20, IsHA: true},     // small / ha
		{ID: 23, CPU: 16, RAM: 32, SSD: 100, IsHA: true},  // large / ha
		{ID: 22, CPU: 8, RAM: 16, SSD: 50, IsHA: true},    // medium / ha
	}

	got := SizeClassByID(flavors)
	want := map[int64]string{
		11: "small", 12: "medium", 13: "large",
		21: "small", 22: "medium", 23: "large",
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("SizeClassByID[%d] = %q, want %q", id, got[id], w)
		}
	}

	// FlavorIDBySize over a single HA-filtered group (how the resource calls it).
	ha := []MasterNodeConfig{
		{ID: 23, CPU: 16, RAM: 32, SSD: 100, IsHA: true},
		{ID: 21, CPU: 2, RAM: 4, SSD: 20, IsHA: true},
		{ID: 22, CPU: 8, RAM: 16, SSD: 50, IsHA: true},
	}
	for size, wantID := range map[string]int64{"small": 21, "medium": 22, "large": 23} {
		gotID, ok := FlavorIDBySize(ha, size)
		if !ok || gotID != wantID {
			t.Errorf("FlavorIDBySize(%q) = %d,%v; want %d,true", size, gotID, ok, wantID)
		}
	}

	// A non-3-tier group has no clean mapping: unmapped, and lookups fail.
	two := []MasterNodeConfig{
		{ID: 1, CPU: 2, RAM: 4, SSD: 20, IsHA: false},
		{ID: 2, CPU: 8, RAM: 16, SSD: 50, IsHA: false},
	}
	if m := SizeClassByID(two); len(m) != 0 {
		t.Errorf("SizeClassByID(2 flavors) = %v, want empty", m)
	}
	if _, ok := FlavorIDBySize(two, "small"); ok {
		t.Error("FlavorIDBySize on a non-3-tier group should fail")
	}
}
