package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// kuberBasePath is the class-level @RequestMapping prefix of panel-main's
// KuberController. Every Managed Kubernetes endpoint hangs off it.
const kuberBasePath = "/api/kubernetes"

// Cluster lifecycle statuses (the name field of panel-main's nested Statuses
// entity). SUCCESS and FAIL are terminal; DELETED is returned forever by
// getCluster after a soft-delete (there is no 404), so Read maps it to "gone".
const (
	ClusterStatusNew        = "NEW"
	ClusterStatusProcessing = "PROCESSING"
	ClusterStatusSuccess    = "SUCCESS"
	ClusterStatusFail       = "FAIL"
	ClusterStatusDeleted    = "DELETED"
)

// Cluster is the decoded, flattened form of panel-main's ClusterDTO.
//
// Note the fields ClusterDTO does NOT carry (unlike LoadBalancerDTO): there is
// no region, project_tag or node_ip_range on the wire — only a numeric
// ProjectID. The provider sources region/project from configuration (the request
// headers), and node_ip_range is a write-once create input that cannot be read
// back.
type Cluster struct {
	ID                int64
	Name              string
	Status            string // status.name — NEW|PROCESSING|SUCCESS|FAIL|DELETED
	KubeVersion       string // kuberVersion (NOT "kubernetes_version")
	APIEndpoint       string
	IsPublic          bool
	IsHA              bool
	PodSubnet         string
	NodeIPRange       string // nodeIpRange — start-end; echoed by the API since the backend may auto-allocate it
	Kubeconfig        string // clusterConfigSecret — may be empty while NEW/PROCESSING
	SSHKeyEncoded     string
	PrivateKeyEncoded string
	Blocked           bool
	IPAddressesCount  int64
	ProjectID         int64
	DateCreated       string // ISO-8601 offset string in UTC
	Description       string
	// Counts are populated only by getCluster / getClusters; on the create
	// response they are absent (null) and decode to zero.
	MasterNodeCount int
	WorkerNodeCount int
	NodePoolCount   int
	// MasterNodeConfig is the cluster's master sizing (flat MasterNodeConfigDTO
	// since G13). Its ID is the master_flavor_id the provider round-trips.
	MasterNodeConfig *MasterNodeConfig
}

// NodePool is the decoded, flattened form of panel-main's NodePoolDTO.
type NodePool struct {
	ID         int64
	Name       string // poolName
	NodeCount  int
	NodeSubnet int
	CPU        int
	// RAM is the raw wire value from k8s_node_pools.ram. The create path accepts
	// workerRam in GB; whether the stored value reads back in GB or MB is not yet
	// verified on the wire (the resource layer owns unit handling — see step 1.5).
	RAM              int
	SSD              int
	Status           string // status.name; pools are PROCESSING<->SUCCESS, never FAIL/DELETED
	ClusterID        int64
	AutoscaleEnabled bool
	MinNodes         int
	MaxNodes         int
}

// MasterNodeConfig is the flat master-node sizing record (panel-main's
// MasterNodeConfigDTO, introduced for G11/G13). It backs the flavors data source
// and identifies a cluster's master_flavor_id.
type MasterNodeConfig struct {
	ID              int64
	CPU             int
	RAM             int
	SSD             int
	IsHA            bool
	RegionID        int64
	OperationTypeID int64
	ResourceID      int64
}

// KuberVersion is one selectable Kubernetes version (panel-main's
// KubernetesVersion entity). IsDebug versions are internal and should be hidden
// by the versions data source unless explicitly requested.
type KuberVersion struct {
	ID      int64
	Version string
	IsDebug bool
}

// ---- request bodies (mirror panel-main's create/modify DTOs exactly) ----

// CreateClusterRequest is the body for POST /createCluster (NewClusterCreateDTO).
// Dead fields gateway/prefix are intentionally omitted (gateway is taken from the
// local network server-side; serviceSubnet is not a DTO field). nodeSubnet is also
// omitted: panel-main derives the node subnet prefix from the local network's own
// mask, so the inbound value was never authoritative for addressing. Addresses
// (node_ip_range) is omitempty: when the caller omits it, panel-main auto-allocates a
// free contiguous range from the local network and echoes it back on the cluster
// (nodeIpRange). The region and project are NOT in the body: createCluster hardcodes
// the user's current region (K8SCluster ctor), so the cluster lands in whatever region
// the caller's headers resolve to.
type CreateClusterRequest struct {
	ClusterName        string   `json:"clusterName"`
	WorkerDiskSize     int      `json:"workerDiskSize"`
	WorkerCPU          int      `json:"workerCpu"`
	WorkerRAM          int      `json:"workerRam"`
	WorkerReplicas     int      `json:"workerReplicas"`
	Addresses          []string `json:"addresses,omitempty"`
	KuberVersion       string   `json:"kuberVersion"`
	NodePoolName       string   `json:"nodePoolName"`
	NeedPublicIP       bool     `json:"needPublicIp"`
	PublicKey          string   `json:"publicKey,omitempty"`
	AuthorizeSSH       bool     `json:"authorizeSsh"`
	PodSubnet          string   `json:"podSubnet"`
	LocalNetID         int64    `json:"localNetId"`
	IsHA               bool     `json:"isHa"`
	MasterNodeConfigID int64    `json:"masterNodeConfigId"`
	AutoScaleEnabled   bool     `json:"autoScaleEnabled"`
	MaxNodes           int      `json:"maxNodes,omitempty"`
	MinNodes           int      `json:"minNodes,omitempty"`
	Description        string   `json:"description,omitempty"`
}

// CreateNodePoolRequest is the body for POST /createNewNodePool
// (CreateK8SNodePoolDTO). Only the fields below are honored; namespace,
// clusterName, vlans, kuberVersion, clientNetName and templateNode are all
// overwritten server-side from the cluster, so they are omitted.
type CreateNodePoolRequest struct {
	ClusterID        int64  `json:"clusterId"`
	NodePoolName     string `json:"nodePoolName"`
	WorkerReplicas   int    `json:"workerReplicas"`
	WorkerCPU        int    `json:"workerCpu"`
	WorkerRAM        int    `json:"workerRam"`
	WorkerDiskSize   int    `json:"workerDiskSize"`
	AutoScaleEnabled bool   `json:"autoScaleEnabled"`
	MinNodes         int    `json:"minNodes,omitempty"`
	MaxNodes         int    `json:"maxNodes,omitempty"`
}

// ModifyNodePoolRequest is the body for changeNodePoolSize, enableAutoscaling and
// updateAutoscaling (all panel-main's ModifyKuberClusterDTO). changeNodePoolSize
// reads ClusterID/NodePoolID/WorkerReplicas; the autoscaling endpoints read
// ClusterID/NodePoolID/MinNodes/MaxNodes.
type ModifyNodePoolRequest struct {
	ClusterID      int64 `json:"clusterId"`
	NodePoolID     int64 `json:"nodePoolId"`
	WorkerReplicas int   `json:"workerReplicas,omitempty"`
	MinNodes       int   `json:"minNodes,omitempty"`
	MaxNodes       int   `json:"maxNodes,omitempty"`
}

// DisableAutoscalerRequest is the body for POST /disableAutoscaler
// (DisableAutoscalerDTO). FixedSize, when non-zero, pins the pool's node count.
type DisableAutoscalerRequest struct {
	ClusterID      int64 `json:"clusterId"`
	NodePoolID     int64 `json:"nodePoolId"`
	FixedSize      int   `json:"fixedSize,omitempty"`
	WorkerReplicas int   `json:"workerReplicas,omitempty"`
}

// UpdateMasterConfigRequest is the body for PATCH /updateMasterNodeConfig
// (the UpdateMasterNodeConfigRequest record: {clusterId, masterNodeConfigId}).
type UpdateMasterConfigRequest struct {
	ClusterID          int64 `json:"clusterId"`
	MasterNodeConfigID int64 `json:"masterNodeConfigId"`
}

// ---- wire types: mirror panel-main's DTOs field-for-field ----
//
// Field names are byte-for-byte the JSON keys panel-main emits (Lombok @Data +
// Jackson default naming, no @JsonProperty overrides). A wrong key silently
// decodes to a zero value — the exact defect class that bit the LB GUID work — so
// these are pinned by golden wire tests in kuber_test.go.

type kuberStatusDTO struct {
	Name string `json:"name"`
}

type masterNodeConfigDTO struct {
	ID              int64 `json:"id"`
	CPU             int   `json:"cpu"`
	RAM             int   `json:"ram"`
	SSD             int   `json:"ssd"`
	IsHA            bool  `json:"isHa"`
	RegionID        int64 `json:"regionId"`
	OperationTypeID int64 `json:"operationTypeId"`
	ResourceID      int64 `json:"resourceId"`
}

type clusterDTO struct {
	ID                  int64                `json:"id"`
	Status              kuberStatusDTO       `json:"status"`
	KuberVersion        string               `json:"kuberVersion"`
	Name                string               `json:"name"`
	APIEndpoint         string               `json:"apiEndpoint"`
	IsPublic            bool                 `json:"isPublic"`
	SSHKeyEncoded       string               `json:"sshKeyEncoded"`
	PrivateKeyEncoded   string               `json:"privateKeyEncoded"`
	ClusterConfigSecret string               `json:"clusterConfigSecret"`
	IsHA                bool                 `json:"isHa"`
	MasterNodeCount     *int                 `json:"masterNodeCount"`
	WorkerNodeCount     *int                 `json:"workerNodeCount"`
	NodePoolCount       *int                 `json:"nodePoolCount"`
	MasterNodeConfig    *masterNodeConfigDTO `json:"masterNodeConfiguration"`
	PodSubnet           string               `json:"podSubnet"`
	NodeIPRange         string               `json:"nodeIpRange"`
	Blocked             bool                 `json:"blocked"`
	IPAddressesCount    int64                `json:"ipAddressesCount"`
	ProjectID           int64                `json:"projectId"`
	DateCreated         string               `json:"dateCreated"`
	Description         string               `json:"description"`
}

type nodePoolDTO struct {
	ID               int64          `json:"id"`
	PoolName         string         `json:"poolName"`
	NodeCount        int            `json:"nodeCount"`
	NodeSubnet       int            `json:"nodeSubnet"`
	CPU              int            `json:"cpu"`
	RAM              int            `json:"ram"`
	SSD              int            `json:"ssd"`
	Status           kuberStatusDTO `json:"status"`
	ClusterID        int64          `json:"clusterId"`
	AutoscaleEnabled bool           `json:"autoscaleEnabled"`
	MinNodes         *int           `json:"minNodes"`
	MaxNodes         *int           `json:"maxNodes"`
}

type kuberVersionDTO struct {
	ID      int64  `json:"id"`
	Version string `json:"version"`
	IsDebug bool   `json:"isDebug"`
}

func (d *masterNodeConfigDTO) toMasterNodeConfig() *MasterNodeConfig {
	if d == nil {
		return nil
	}
	return &MasterNodeConfig{
		ID:              d.ID,
		CPU:             d.CPU,
		RAM:             d.RAM,
		SSD:             d.SSD,
		IsHA:            d.IsHA,
		RegionID:        d.RegionID,
		OperationTypeID: d.OperationTypeID,
		ResourceID:      d.ResourceID,
	}
}

func (d *clusterDTO) toCluster() *Cluster {
	return &Cluster{
		ID:                d.ID,
		Name:              d.Name,
		Status:            d.Status.Name,
		KubeVersion:       d.KuberVersion,
		APIEndpoint:       d.APIEndpoint,
		IsPublic:          d.IsPublic,
		IsHA:              d.IsHA,
		PodSubnet:         d.PodSubnet,
		NodeIPRange:       d.NodeIPRange,
		Kubeconfig:        d.ClusterConfigSecret,
		SSHKeyEncoded:     d.SSHKeyEncoded,
		PrivateKeyEncoded: d.PrivateKeyEncoded,
		Blocked:           d.Blocked,
		IPAddressesCount:  d.IPAddressesCount,
		ProjectID:         d.ProjectID,
		DateCreated:       d.DateCreated,
		Description:       d.Description,
		MasterNodeCount:   derefInt(d.MasterNodeCount),
		WorkerNodeCount:   derefInt(d.WorkerNodeCount),
		NodePoolCount:     derefInt(d.NodePoolCount),
		MasterNodeConfig:  d.MasterNodeConfig.toMasterNodeConfig(),
	}
}

func (d *nodePoolDTO) toNodePool() *NodePool {
	return &NodePool{
		ID:               d.ID,
		Name:             d.PoolName,
		NodeCount:        d.NodeCount,
		NodeSubnet:       d.NodeSubnet,
		CPU:              d.CPU,
		RAM:              d.RAM,
		SSD:              d.SSD,
		Status:           d.Status.Name,
		ClusterID:        d.ClusterID,
		AutoscaleEnabled: d.AutoscaleEnabled,
		MinNodes:         derefInt(d.MinNodes),
		MaxNodes:         derefInt(d.MaxNodes),
	}
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// ---- transport ----

// withEnglishLang returns a copy of opts with Lang forced to "en" (unless the
// caller already set a language). The kuber endpoints distinguish business
// errors only by their errMessage text (ADR-K1), so the provider pins them to
// English to keep its matchers stable regardless of the API key's user language.
func withEnglishLang(opts *RequestOpts) *RequestOpts {
	out := RequestOpts{Lang: "en"}
	if opts != nil {
		out.Region = opts.Region
		out.ProjectTag = opts.ProjectTag
		if opts.Lang != "" {
			out.Lang = opts.Lang
		}
	}
	return &out
}

// doKuberV1 issues a request to a kuber (V1-style) endpoint and decodes the
// envelope into T. panel-main's kuber controller is dual-envelope: success and
// most business errors come back as a V1 RestResponse {error,errMessage,data}
// (the latter at HTTP 500), while a handful of paths (codes 756/757, header
// resolution) throw ApiException and render the V2 {success,errors[]} envelope.
// parseV1Response handles both.
func doKuberV1[T any](ctx context.Context, c *Client, method, path string, body any, opts *RequestOpts) (T, error) {
	var zero T
	statusCode, respBody, err := c.doRequest(ctx, method, kuberBasePath+path, body, withEnglishLang(opts))
	if err != nil {
		return zero, err
	}
	data, apiErr := parseV1Response[T](statusCode, respBody)
	if apiErr != nil {
		return zero, apiErr
	}
	return data, nil
}

func singleCluster(dto *clusterDTO, err error, op string) (*Cluster, error) {
	if err != nil {
		return nil, err
	}
	if dto == nil {
		return nil, fmt.Errorf("%s: empty response from server", op)
	}
	return dto.toCluster(), nil
}

func singleNodePool(dto *nodePoolDTO, err error, op string) (*NodePool, error) {
	if err != nil {
		return nil, err
	}
	if dto == nil {
		return nil, fmt.Errorf("%s: empty response from server", op)
	}
	return dto.toNodePool(), nil
}

// ---- error helpers ----

// kuberNotFoundMessages are the errMessage strings panel-main returns (at HTTP
// 500, V1 envelope) for a missing cluster or node pool. The kuber endpoints do
// NOT use the standard not-found codes (601/703/628), so the provider matches on
// these strings — pinned to English via X-Lang and captured in step 0.0's
// baseline. (Cross-project access is reported as not-found by these endpoints,
// which is acceptable here: ownership is enforced and there is no distinct code.)
var kuberNotFoundMessages = []string{
	"Cluster not found!",
	"Could not find cluster",
	"Could not find the cluster",
	"K8SCluster not found!",
	"NodePool not found!",
	"Node pool not found",
	"Could not find the nodePool",
}

// IsKuberNotFound reports whether err indicates a missing kuber cluster or node
// pool. Note: a soft-deleted cluster is NOT reported here — getCluster returns it
// with status DELETED (HTTP 200); callers detect that via Cluster.Status.
func IsKuberNotFound(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.StatusCode == http.StatusNotFound {
		return true
	}
	for _, s := range kuberNotFoundMessages {
		if strings.Contains(apiErr.Message, s) {
			return true
		}
	}
	return false
}

// IsLastWorkerPool reports whether err is the "cannot delete the last worker node
// pool" guard (code 756, G1).
func IsLastWorkerPool(err error) bool { return IsAPIError(err, 756) }

// IsVersionUnavailable reports whether err is the "version not available in this
// region" error (code 757, G10).
func IsVersionUnavailable(err error) bool { return IsAPIError(err, 757) }

// KuberErrorDetail maps known kuber failures to a clean, user-facing message for
// Terraform diagnostics. Unrecognized errors fall through to err.Error().
func KuberErrorDetail(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.HasCode(756):
			return "Cannot delete the last worker node pool of a cluster. Destroy the whole cluster instead, or add another worker pool first."
		case apiErr.HasCode(757):
			return "The requested Kubernetes version is not available in this region. Choose one returned by the prodata_kubernetes_versions data source."
		case apiErr.HasCode(638):
			return "The X-Region could not be resolved. Check the provider/region configuration."
		case apiErr.HasCode(645):
			return "The project tag could not be resolved. Check the provider/project configuration."
		}
		switch {
		case strings.Contains(apiErr.Message, "Cluster with this name already exist"):
			return "A Kubernetes cluster with this name already exists in this region. Choose a different name."
		case strings.Contains(apiErr.Message, "Node pool with this name already exists"):
			return "A node pool with this name already exists in this cluster. Choose a different name."
		case strings.Contains(apiErr.Message, "Cannot delete master node pool"):
			return "This node pool is a control-plane (master) pool and cannot be deleted via this resource."
		}
	}
	return err.Error()
}

// ---- cluster operations ----

// CreateCluster provisions a new cluster (plus its inline default node pool). The
// response carries the new cluster's id; the counts and kubeconfig are populated
// asynchronously by the backend reconciler and must be polled via GetCluster.
func (c *Client) CreateCluster(ctx context.Context, req CreateClusterRequest, opts *RequestOpts) (*Cluster, error) {
	dto, err := doKuberV1[*clusterDTO](ctx, c, http.MethodPost, "/createCluster", req, opts)
	return singleCluster(dto, err, "create cluster")
}

// GetCluster fetches a cluster by id. A soft-deleted cluster is returned with
// status DELETED (there is no 404); callers map that to "resource gone".
func (c *Client) GetCluster(ctx context.Context, id int64, opts *RequestOpts) (*Cluster, error) {
	path := fmt.Sprintf("/getCluster/%d", id)
	dto, err := doKuberV1[*clusterDTO](ctx, c, http.MethodGet, path, nil, opts)
	return singleCluster(dto, err, fmt.Sprintf("get cluster %d", id))
}

// ListClusters returns every non-DELETED cluster visible to the resolved
// region+project (getClusters honors X-Region / X-Project-Tag). Used by the
// cluster data source and for adopt-or-error after a lost create response.
func (c *Client) ListClusters(ctx context.Context, opts *RequestOpts) ([]Cluster, error) {
	dtos, err := doKuberV1[[]clusterDTO](ctx, c, http.MethodGet, "/getClusters", nil, opts)
	if err != nil {
		return nil, err
	}
	out := make([]Cluster, 0, len(dtos))
	for i := range dtos {
		out = append(out, *dtos[i].toCluster())
	}
	return out, nil
}

// DeleteCluster soft-deletes a cluster (synchronous on the backend ack; infra
// teardown continues in the background). Idempotent.
func (c *Client) DeleteCluster(ctx context.Context, id int64, opts *RequestOpts) error {
	path := fmt.Sprintf("/deleteCluster/%d", id)
	_, err := doKuberV1[json.RawMessage](ctx, c, http.MethodPost, path, nil, opts)
	return err
}

// UpdateClusterVersion upgrades a cluster's Kubernetes version (async — the
// cluster and its pools go PROCESSING). The new version is a query parameter.
func (c *Client) UpdateClusterVersion(ctx context.Context, id int64, version string, opts *RequestOpts) (*Cluster, error) {
	path := fmt.Sprintf("/updateClusterKuberVersion/%d?", id) +
		url.Values{"version": {version}}.Encode()
	dto, err := doKuberV1[*clusterDTO](ctx, c, http.MethodPost, path, nil, opts)
	return singleCluster(dto, err, fmt.Sprintf("update cluster %d version", id))
}

// UpdateMasterConfig changes a cluster's master-node sizing (async). The cluster
// goes PROCESSING while the control-plane rolls.
func (c *Client) UpdateMasterConfig(ctx context.Context, req UpdateMasterConfigRequest, opts *RequestOpts) error {
	_, err := doKuberV1[json.RawMessage](ctx, c, http.MethodPatch, "/updateMasterNodeConfig", req, opts)
	return err
}

// ---- node pool operations ----

// CreateNodePool adds a worker node pool to a cluster. panel-main returns no id
// for the new pool, so the caller discovers it via an id-set diff over
// ListNodePools (ADR-K6).
func (c *Client) CreateNodePool(ctx context.Context, req CreateNodePoolRequest, opts *RequestOpts) error {
	_, err := doKuberV1[json.RawMessage](ctx, c, http.MethodPost, "/createNewNodePool", req, opts)
	return err
}

// GetNodePool fetches a single node pool by id.
func (c *Client) GetNodePool(ctx context.Context, id int64, opts *RequestOpts) (*NodePool, error) {
	path := fmt.Sprintf("/getK8SNodePool/%d", id)
	dto, err := doKuberV1[*nodePoolDTO](ctx, c, http.MethodGet, path, nil, opts)
	return singleNodePool(dto, err, fmt.Sprintf("get node pool %d", id))
}

// ListNodePools returns the node pools of a cluster.
func (c *Client) ListNodePools(ctx context.Context, clusterID int64, opts *RequestOpts) ([]NodePool, error) {
	path := fmt.Sprintf("/getNodePoolsByClusters/%d", clusterID)
	dtos, err := doKuberV1[[]nodePoolDTO](ctx, c, http.MethodGet, path, nil, opts)
	if err != nil {
		return nil, err
	}
	out := make([]NodePool, 0, len(dtos))
	for i := range dtos {
		out = append(out, *dtos[i].toNodePool())
	}
	return out, nil
}

// DeleteNodePool deletes a node pool. The backend refuses to delete the last
// worker pool (code 756 — see IsLastWorkerPool).
func (c *Client) DeleteNodePool(ctx context.Context, id int64, opts *RequestOpts) error {
	path := fmt.Sprintf("/deleteNodePool/%d", id)
	_, err := doKuberV1[json.RawMessage](ctx, c, http.MethodPost, path, nil, opts)
	return err
}

// ChangeNodePoolSize sets a (non-autoscaling) pool's node count.
func (c *Client) ChangeNodePoolSize(ctx context.Context, req ModifyNodePoolRequest, opts *RequestOpts) error {
	_, err := doKuberV1[json.RawMessage](ctx, c, http.MethodPost, "/changeNodePoolSize", req, opts)
	return err
}

// EnableAutoscaling turns on autoscaling for a pool with the given bounds.
func (c *Client) EnableAutoscaling(ctx context.Context, req ModifyNodePoolRequest, opts *RequestOpts) error {
	_, err := doKuberV1[json.RawMessage](ctx, c, http.MethodPost, "/enableAutoscaling", req, opts)
	return err
}

// UpdateAutoscaling changes the bounds of an already-autoscaling pool.
func (c *Client) UpdateAutoscaling(ctx context.Context, req ModifyNodePoolRequest, opts *RequestOpts) error {
	_, err := doKuberV1[json.RawMessage](ctx, c, http.MethodPost, "/updateAutoscaling", req, opts)
	return err
}

// DisableAutoscaler turns off autoscaling for a pool, optionally pinning it to a
// fixed size.
func (c *Client) DisableAutoscaler(ctx context.Context, req DisableAutoscalerRequest, opts *RequestOpts) error {
	_, err := doKuberV1[json.RawMessage](ctx, c, http.MethodPost, "/disableAutoscaler", req, opts)
	return err
}

// ---- data-source reads ----

// ListKuberVersions returns all selectable Kubernetes versions. The list is
// region-blind and includes isDebug builds; the versions data source filters and
// sorts (ADR / D3).
func (c *Client) ListKuberVersions(ctx context.Context, opts *RequestOpts) ([]KuberVersion, error) {
	dtos, err := doKuberV1[[]kuberVersionDTO](ctx, c, http.MethodGet, "/getKuberVersions", nil, opts)
	if err != nil {
		return nil, err
	}
	out := make([]KuberVersion, 0, len(dtos))
	for _, d := range dtos {
		out = append(out, KuberVersion(d))
	}
	return out, nil
}

// GetMasterNodeConfigs returns the available master-node sizings for the resolved
// region (getMasterNodeConfig honors X-Region), filtered by HA. The flavors data
// source calls it once per is_ha value and merges.
func (c *Client) GetMasterNodeConfigs(ctx context.Context, isHA bool, opts *RequestOpts) ([]MasterNodeConfig, error) {
	path := "/getMasterNodeConfig/" + strconv.FormatBool(isHA)
	dtos, err := doKuberV1[[]masterNodeConfigDTO](ctx, c, http.MethodGet, path, nil, opts)
	if err != nil {
		return nil, err
	}
	out := make([]MasterNodeConfig, 0, len(dtos))
	for i := range dtos {
		out = append(out, *dtos[i].toMasterNodeConfig())
	}
	return out, nil
}

// ControlPlaneSizes is the ordered control-plane size ladder, smallest first. It maps
// onto the master-flavor catalog by capacity rank rather than by absolute specs, so the
// mapping is stable even if a tier's CPU/RAM is later retuned.
var ControlPlaneSizes = []string{"small", "medium", "large"}

// SizeClassByID maps each master flavor to a control-plane size (small/medium/large) by
// its capacity rank, computed independently within each HA mode (the catalog is a fixed
// 3-tier ladder per region+HA). A flavor whose HA group is not exactly a 3-tier ladder is
// omitted from the result, so callers can detect "no clean mapping" by a missing id.
func SizeClassByID(flavors []MasterNodeConfig) map[int64]string {
	byHA := map[bool][]MasterNodeConfig{}
	for _, f := range flavors {
		byHA[f.IsHA] = append(byHA[f.IsHA], f)
	}
	out := make(map[int64]string, len(flavors))
	for _, group := range byHA {
		if len(group) != len(ControlPlaneSizes) {
			continue // not a clean small/medium/large ladder — leave these unmapped
		}
		sorted := append([]MasterNodeConfig(nil), group...)
		sort.Slice(sorted, func(i, j int) bool {
			if sorted[i].CPU != sorted[j].CPU {
				return sorted[i].CPU < sorted[j].CPU
			}
			if sorted[i].RAM != sorted[j].RAM {
				return sorted[i].RAM < sorted[j].RAM
			}
			return sorted[i].SSD < sorted[j].SSD
		})
		for i := range sorted {
			out[sorted[i].ID] = ControlPlaneSizes[i]
		}
	}
	return out
}

// FlavorIDBySize resolves a control-plane size (small/medium/large) to a master flavor ID
// within the given HA-filtered flavor list. The second return is false when the list is not
// a clean 3-tier ladder or the size is unknown.
func FlavorIDBySize(flavors []MasterNodeConfig, size string) (int64, bool) {
	for id, s := range SizeClassByID(flavors) {
		if s == size {
			return id, true
		}
	}
	return 0, false
}
