package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Load balancer type values (the prodata_lb `type` attribute). On the wire panel-main
// uses a boolean `isPublic`; the client maps it to these strings.
const (
	LbTypeExternal = "external"
	LbTypeInternal = "internal"
)

// Load balancer protocol values. The panel stores protocol as an unnormalized
// string column, so legacy load balancers may read back as lowercase; the
// adapter normalizes to upper-case on the way out (see lbDTO.toLoadBalancer).
const (
	LbProtocolTCP = "TCP"
	LbProtocolUDP = "UDP"
)

// Load balancer source values. Authoritative for routing configure/delete calls:
// a CCM-source LB must use the /api/ccm/loadbalancer/* endpoints.
const (
	LbSourceFrontend = "FRONTEND"
	LbSourceCCM      = "CCM"
)

// Load balancer lifecycle statuses (flattened from panel-main's nested Statuses
// entity). SUCCESS and FAIL are terminal; FAIL is a terminal error state.
const (
	LbStatusNew        = "NEW"
	LbStatusProcessing = "PROCESSING"
	LbStatusSuccess    = "SUCCESS"
	LbStatusDeleted    = "DELETED"
	LbStatusFail       = "FAIL"
)

// LoadBalancer is the decoded, flattened form of panel-main's LoadBalancerDTO.
type LoadBalancer struct {
	ID          int64
	Name        string
	Description string
	Type        string // "external" | "internal"
	Source      string // "FRONTEND" | "CCM" — empty for legacy LBs created before sourcing
	Status      string // NEW | PROCESSING | SUCCESS | DELETED | FAIL
	Protocol    string
	NetworkID   int64
	PublicIP    string // provisioned public IP; empty for internal LBs or while NEW
	PrivateIP   string // VIP inside the local network; may be empty while NEW
	DateCreated string
	Backends    []LbBackend
	Ports       []LbPort
	Region      string
	ProjectTag  string
}

// LbBackend is one backend member of a load balancer. Backends are identified by
// VM guid (see prodata_vm's computed `guid` attribute).
type LbBackend struct {
	Guid   string
	Status string
	IP     string
}

// LbPort is one port mapping: traffic to Port on the balancer is forwarded to
// TargetPort on the backends.
type LbPort struct {
	Port       int32
	TargetPort int32
}

// LbBackendRef references a backend VM by its guid in a create/configure request.
type LbBackendRef struct {
	UserVmID string `json:"userVmId"`
}

// LbPortReq is a port mapping in a create/configure request.
type LbPortReq struct {
	BalancerPort int32 `json:"balancerPort"`
	BackendPort  int32 `json:"backendPort"`
}

// LoadBalancerRequest is the body for the Frontend create endpoint and for both
// the Frontend and CCM configure endpoints — panel-main accepts the same
// LoadBalancerCreateRequest shape for all three.
//
// loadBalancerVip and extraIps are intentionally not modeled: panel-main
// auto-allocates them from the network when absent, and pinning them would
// couple Terraform state to network-internal addressing.
type LoadBalancerRequest struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	IsPublic    bool           `json:"isPublic"`
	Protocol    string         `json:"protocol"`
	UserNetID   int64          `json:"userNetId"`
	Backends    []LbBackendRef `json:"backends,omitempty"`
	Ports       []LbPortReq    `json:"ports"`
}

// CreateLoadBalancerCCMRequest is the body for the CCM create endpoint. The
// backend is a whole K8S node pool, selected by NodePoolID.
type CreateLoadBalancerCCMRequest struct {
	Name       string      `json:"name"`
	NodePoolID int64       `json:"nodePoolId"`
	IsPublic   bool        `json:"isPublic"`
	Protocol   string      `json:"protocol"`
	Ports      []LbPortReq `json:"ports"`
}

// ---- wire types: mirror panel-main's LoadBalancerDTO exactly ----

type lbDTO struct {
	ID                   int64          `json:"id"`
	Name                 string         `json:"name"`
	Description          string         `json:"description"`
	IsPublic             bool           `json:"isPublic"`
	Status               lbStatusDTO    `json:"status"`
	UserNet              int64          `json:"userNet"`
	DateCreated          string         `json:"dateCreated"`
	LocalUserVip         string         `json:"localUserVip"`
	ProvisioningPublicIP string         `json:"provisioningPublicIp"`
	Protocol             string         `json:"protocol"`
	Backends             []lbBackendDTO `json:"backends"`
	Ports                []lbPortDTO    `json:"ports"`
	Source               string         `json:"source"`
	Region               string         `json:"region"`
	ProjectTag           string         `json:"projectTag"`
}

type lbStatusDTO struct {
	Name string `json:"name"`
}

type lbBackendDTO struct {
	IP     string         `json:"ip"`
	Status lbStatusDTO    `json:"status"`
	UserVM lbBackendVMDTO `json:"userVM"`
}

type lbBackendVMDTO struct {
	Guid string `json:"guid"`
}

type lbPortDTO struct {
	SrcPort int32 `json:"srcPort"`
	DstPort int32 `json:"dstPort"`
}

func (d *lbDTO) toLoadBalancer() *LoadBalancer {
	// Normalize protocol to upper-case: the panel stores it unnormalized, so
	// legacy LBs may read back as "tcp"/"udp" and would otherwise diverge from
	// the schema's stringvalidator.OneOf("TCP","UDP") and trigger a spurious
	// RequiresReplace destroy+recreate on the next plan.
	lb := &LoadBalancer{
		ID:          d.ID,
		Name:        d.Name,
		Description: d.Description,
		Type:        lbTypeFromIsPublic(d.IsPublic),
		Source:      d.Source,
		Status:      d.Status.Name,
		Protocol:    strings.ToUpper(d.Protocol),
		NetworkID:   d.UserNet,
		PublicIP:    d.ProvisioningPublicIP,
		PrivateIP:   d.LocalUserVip,
		DateCreated: d.DateCreated,
		Region:      d.Region,
		ProjectTag:  d.ProjectTag,
	}
	for _, b := range d.Backends {
		lb.Backends = append(lb.Backends, LbBackend{
			Guid:   b.UserVM.Guid,
			Status: b.Status.Name,
			IP:     b.IP,
		})
	}
	for _, p := range d.Ports {
		lb.Ports = append(lb.Ports, LbPort{Port: p.SrcPort, TargetPort: p.DstPort})
	}
	return lb
}

func lbTypeFromIsPublic(isPublic bool) string {
	if isPublic {
		return LbTypeExternal
	}
	return LbTypeInternal
}

// doLBV1 issues a request to a V1 (load-balancer) endpoint and decodes the V1
// envelope into T.
func doLBV1[T any](ctx context.Context, c *Client, method, path string, body any, opts *RequestOpts) (T, error) {
	var zero T
	statusCode, respBody, err := c.doRequest(ctx, method, path, body, opts)
	if err != nil {
		return zero, err
	}
	data, apiErr := parseV1Response[T](statusCode, respBody)
	if apiErr != nil {
		return zero, apiErr
	}
	return data, nil
}

func singleLB(dto *lbDTO, err error, op string) (*LoadBalancer, error) {
	if err != nil {
		return nil, err
	}
	if dto == nil {
		return nil, fmt.Errorf("%s: empty response from server", op)
	}
	return dto.toLoadBalancer(), nil
}

// LBErrorDetail maps known load-balancer error codes to a clean, user-facing
// message so Terraform diagnostics don't surface the raw "api error [code]
// (http ...)" form. Unrecognized errors fall through to err.Error().
func LBErrorDetail(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.HasCode(701):
			return "A load balancer with this name already exists in this region. Choose a different name."
		case apiErr.HasCode(736):
			return "Load balancer not found."
		case apiErr.HasCode(737):
			return "The local network does not have enough free IPs. A load balancer needs at least three: one VIP plus two for the hidden nginx VMs."
		case apiErr.HasCode(743):
			return "No IP pool is available for load balancers in this region. This is usually transient capacity; retry shortly or contact support if it persists."
		case apiErr.HasCode(744):
			return "No compute capacity is available to provision the load balancer's nginx VMs. This is usually transient; retry shortly or contact support if it persists."
		case apiErr.HasCode(747):
			return "The load balancer could not be provisioned: the infrastructure (VM provisioning) service reported an error. Retry shortly, and contact support if it persists."
		case apiErr.HasCode(748):
			return "Backend VM not found. The vm_ids set must contain VM guids (the guid attribute of prodata_vm, e.g. prodata_vm.web[*].guid), not numeric ids."
		}
	}
	return err.Error()
}

// CreateLoadBalancerFrontend creates a VM-backed load balancer.
func (c *Client) CreateLoadBalancerFrontend(ctx context.Context, req LoadBalancerRequest, opts *RequestOpts) (*LoadBalancer, error) {
	dto, err := doLBV1[*lbDTO](ctx, c, http.MethodPost, "/api/loadbalancer/createLoadbalancer", req, opts)
	return singleLB(dto, err, "create load balancer")
}

// CreateLoadBalancerCCM creates a K8S-node-pool-backed load balancer.
func (c *Client) CreateLoadBalancerCCM(ctx context.Context, req CreateLoadBalancerCCMRequest, opts *RequestOpts) (*LoadBalancer, error) {
	dto, err := doLBV1[*lbDTO](ctx, c, http.MethodPost, "/api/ccm/loadbalancer/create", req, opts)
	return singleLB(dto, err, "create load balancer")
}

// GetLoadBalancer fetches a load balancer by id. The same endpoint serves both
// Frontend- and CCM-source LBs; the decoded LoadBalancer.Source distinguishes them.
func (c *Client) GetLoadBalancer(ctx context.Context, id int64, opts *RequestOpts) (*LoadBalancer, error) {
	path := "/api/loadbalancer/getLoadBalancer?" +
		url.Values{"loadBalancerId": {strconv.FormatInt(id, 10)}}.Encode()
	dto, err := doLBV1[*lbDTO](ctx, c, http.MethodGet, path, nil, opts)
	return singleLB(dto, err, fmt.Sprintf("get load balancer %d", id))
}

// ListLoadBalancers returns every load balancer visible to the current project.
// The endpoint is not paginated.
func (c *Client) ListLoadBalancers(ctx context.Context, opts *RequestOpts) ([]LoadBalancer, error) {
	dtos, err := doLBV1[[]lbDTO](ctx, c, http.MethodGet, "/api/loadbalancer/getUserLoadBalancers", nil, opts)
	if err != nil {
		return nil, err
	}
	out := make([]LoadBalancer, 0, len(dtos))
	for i := range dtos {
		out = append(out, *dtos[i].toLoadBalancer())
	}
	return out, nil
}

// ConfigureLoadBalancerFrontend updates a Frontend-source load balancer in place.
func (c *Client) ConfigureLoadBalancerFrontend(ctx context.Context, id int64, req LoadBalancerRequest, opts *RequestOpts) (*LoadBalancer, error) {
	path := fmt.Sprintf("/api/loadbalancer/configureLoadbalancer/%d", id)
	dto, err := doLBV1[*lbDTO](ctx, c, http.MethodPost, path, req, opts)
	return singleLB(dto, err, fmt.Sprintf("configure load balancer %d", id))
}

// ConfigureLoadBalancerCCM updates a CCM-source load balancer in place.
func (c *Client) ConfigureLoadBalancerCCM(ctx context.Context, id int64, req LoadBalancerRequest, opts *RequestOpts) (*LoadBalancer, error) {
	path := fmt.Sprintf("/api/ccm/loadbalancer/configureLoadbalancer/%d", id)
	dto, err := doLBV1[*lbDTO](ctx, c, http.MethodPost, path, req, opts)
	return singleLB(dto, err, fmt.Sprintf("configure load balancer %d", id))
}

// DeleteLoadBalancerFrontend deletes a Frontend-source load balancer and its
// hidden service VMs.
func (c *Client) DeleteLoadBalancerFrontend(ctx context.Context, id int64, opts *RequestOpts) error {
	path := fmt.Sprintf("/api/loadbalancer/deleteLoadbalancer/%d", id)
	_, err := doLBV1[json.RawMessage](ctx, c, http.MethodPost, path, nil, opts)
	return err
}

// DeleteLoadBalancerCCM deletes a CCM-source load balancer and its hidden service VMs.
func (c *Client) DeleteLoadBalancerCCM(ctx context.Context, id int64, opts *RequestOpts) error {
	path := fmt.Sprintf("/api/ccm/loadbalancer/deleteLoadbalancer/%d", id)
	_, err := doLBV1[json.RawMessage](ctx, c, http.MethodPost, path, nil, opts)
	return err
}
