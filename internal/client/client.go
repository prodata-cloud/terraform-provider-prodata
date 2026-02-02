package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	baseURL      string
	apiKeyID     string
	apiSecretKey string
	userAgent    string
	Region       string
	ProjectTag   string
	httpClient   *http.Client
}

type Config struct {
	APIBaseURL   string
	APIKeyID     string
	APISecretKey string
	UserAgent    string
	Region       string
	ProjectTag   string
}

func New(cfg Config) (*Client, error) {
	if cfg.APIBaseURL == "" || cfg.APIKeyID == "" || cfg.APISecretKey == "" {
		return nil, fmt.Errorf("api_base_url, api_key_id, and api_secret_key are required")
	}

	return &Client{
		baseURL:      strings.TrimRight(cfg.APIBaseURL, "/") + "/panel-main",
		apiKeyID:     cfg.APIKeyID,
		apiSecretKey: cfg.APISecretKey,
		userAgent:    cfg.UserAgent,
		Region:       cfg.Region,
		ProjectTag:   cfg.ProjectTag,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
	}, nil
}

type apiResponse[T any] struct {
	Success bool       `json:"success"`
	Data    T          `json:"data"`
	Errors  []apiError `json:"errors"`
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// RequestOpts allows per-request overrides of region and project.
type RequestOpts struct {
	Region     string
	ProjectTag string
}

func (c *Client) Do(ctx context.Context, method, path string, body, result any, opts *RequestOpts) error {
	var reqBody io.Reader

	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	fullURL := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, fullURL, reqBody)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	// Determine region and project: use per-request opts if provided, else client defaults.
	region := c.Region
	projectTag := c.ProjectTag
	if opts != nil {
		if opts.Region != "" {
			region = opts.Region
		}
		if opts.ProjectTag != "" {
			projectTag = opts.ProjectTag
		}
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("X-API-KEY", c.apiKeyID)
	req.Header.Set("X-API-SECRET", c.apiSecretKey)
	req.Header.Set("X-Region", region)
	req.Header.Set("X-Project-Tag", projectTag)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	// Check HTTP status code first
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http error %d: %s", resp.StatusCode, string(respBody))
	}

	var apiResp apiResponse[json.RawMessage]
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return fmt.Errorf("parse response (status %d): %s", resp.StatusCode, string(respBody))
	}

	if !apiResp.Success {
		var errMsgs []string
		for _, e := range apiResp.Errors {
			errMsgs = append(errMsgs, e.Message)
		}
		if len(errMsgs) == 0 {
			return fmt.Errorf("api error (status %d): %s", resp.StatusCode, string(respBody))
		}
		return fmt.Errorf("api error: %s", strings.Join(errMsgs, "; "))
	}

	if result != nil {
		if err := json.Unmarshal(apiResp.Data, result); err != nil {
			return fmt.Errorf("parse data: %w", err)
		}
	}

	return nil
}

type Image struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	IsCustom bool   `json:"isCustom"`
}

type ImageQuery struct {
	Slug       string
	Name       string
	Region     string
	ProjectTag string
}

func (c *Client) GetImage(ctx context.Context, q ImageQuery) (*Image, error) {
	params := url.Values{}

	if q.Slug != "" {
		params.Set("slug", q.Slug)
	} else if q.Name != "" {
		params.Set("name", q.Name)
	} else {
		return nil, fmt.Errorf("either slug or name is required")
	}

	opts := &RequestOpts{
		Region:     q.Region,
		ProjectTag: q.ProjectTag,
	}

	var img Image
	if err := c.Do(ctx, http.MethodGet, "/api/v2/image?"+params.Encode(), nil, &img, opts); err != nil {
		return nil, err
	}
	return &img, nil
}

func (c *Client) GetImages(ctx context.Context, opts *RequestOpts) ([]Image, error) {
	var images []Image
	if err := c.Do(ctx, http.MethodGet, "/api/v2/images", nil, &images, opts); err != nil {
		return nil, err
	}
	return images, nil
}

type Volume struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Size       int64  `json:"size"`
	InUse      bool   `json:"inUse"`
	AttachedID *int64 `json:"attachedId"`
}

func (c *Client) GetVolumes(ctx context.Context, opts *RequestOpts) ([]Volume, error) {
	var volumes []Volume
	if err := c.Do(ctx, http.MethodGet, "/api/v2/volumes", nil, &volumes, opts); err != nil {
		return nil, err
	}
	return volumes, nil
}

func (c *Client) GetVolume(ctx context.Context, id int64, opts *RequestOpts) (*Volume, error) {
	var volume Volume
	path := fmt.Sprintf("/api/v2/volumes/%d", id)
	if err := c.Do(ctx, http.MethodGet, path, nil, &volume, opts); err != nil {
		return nil, err
	}
	return &volume, nil
}

type CreateVolumeRequest struct {
	Region     string `json:"region"`
	ProjectTag string `json:"projectTag"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Size       int64  `json:"size"`
}

func (c *Client) CreateVolume(ctx context.Context, req CreateVolumeRequest) (*Volume, error) {
	if req.Region == "" {
		req.Region = c.Region
	}
	if req.ProjectTag == "" {
		req.ProjectTag = c.ProjectTag
	}

	var volume Volume
	if err := c.Do(ctx, http.MethodPost, "/api/v2/volumes", req, &volume, nil); err != nil {
		return nil, err
	}
	return &volume, nil
}

type UpdateVolumeRequest struct {
	Region     string `json:"region,omitempty"`
	ProjectTag string `json:"projectTag,omitempty"`
	Name       string `json:"name"`
}

func (c *Client) UpdateVolume(ctx context.Context, id int64, req UpdateVolumeRequest) (*Volume, error) {
	if req.Region == "" {
		req.Region = c.Region
	}
	if req.ProjectTag == "" {
		req.ProjectTag = c.ProjectTag
	}

	path := fmt.Sprintf("/api/v2/volumes/%d", id)
	var volume Volume
	if err := c.Do(ctx, http.MethodPut, path, req, &volume, nil); err != nil {
		return nil, err
	}
	return &volume, nil
}

func (c *Client) DeleteVolume(ctx context.Context, id int64, opts *RequestOpts) error {
	path := fmt.Sprintf("/api/v2/volumes/%d", id)

	// Only add query params if explicitly provided in opts (overrides provider defaults)
	if opts != nil && (opts.Region != "" || opts.ProjectTag != "") {
		params := url.Values{}
		if opts.Region != "" {
			params.Set("region", opts.Region)
		}
		if opts.ProjectTag != "" {
			params.Set("projectTag", opts.ProjectTag)
		}
		path = path + "?" + params.Encode()
	}

	if err := c.Do(ctx, http.MethodDelete, path, nil, nil, opts); err != nil {
		return err
	}
	return nil
}

// LocalNetwork represents a local network resource.
type LocalNetwork struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	CIDR    string `json:"cidr"`
	Gateway string `json:"gateway"`
	Linked  bool   `json:"linked"`
}

func (c *Client) GetLocalNetworks(ctx context.Context, opts *RequestOpts) ([]LocalNetwork, error) {
	var networks []LocalNetwork
	if err := c.Do(ctx, http.MethodGet, "/api/v2/local-networks", nil, &networks, opts); err != nil {
		return nil, err
	}
	return networks, nil
}

type CreateLocalNetworkRequest struct {
	Region     string `json:"region"`
	ProjectTag string `json:"projectTag"`
	Name       string `json:"name"`
	CIDR       string `json:"cidr"`
	Gateway    string `json:"gateway"`
}

func (c *Client) CreateLocalNetwork(ctx context.Context, req CreateLocalNetworkRequest) (*LocalNetwork, error) {
	if req.Region == "" {
		req.Region = c.Region
	}
	if req.ProjectTag == "" {
		req.ProjectTag = c.ProjectTag
	}

	var network LocalNetwork
	if err := c.Do(ctx, http.MethodPost, "/api/v2/local-networks", req, &network, nil); err != nil {
		return nil, err
	}
	return &network, nil
}

func (c *Client) GetLocalNetwork(ctx context.Context, id int64, opts *RequestOpts) (*LocalNetwork, error) {
	var network LocalNetwork
	path := fmt.Sprintf("/api/v2/local-networks/%d", id)
	if err := c.Do(ctx, http.MethodGet, path, nil, &network, opts); err != nil {
		return nil, err
	}
	return &network, nil
}

type UpdateLocalNetworkRequest struct {
	Region     string `json:"region,omitempty"`
	ProjectTag string `json:"projectTag,omitempty"`
	Name       string `json:"name"`
}

func (c *Client) UpdateLocalNetwork(ctx context.Context, id int64, req UpdateLocalNetworkRequest) (*LocalNetwork, error) {
	if req.Region == "" {
		req.Region = c.Region
	}
	if req.ProjectTag == "" {
		req.ProjectTag = c.ProjectTag
	}

	path := fmt.Sprintf("/api/v2/local-networks/%d", id)
	var network LocalNetwork
	if err := c.Do(ctx, http.MethodPut, path, req, &network, nil); err != nil {
		return nil, err
	}
	return &network, nil
}

func (c *Client) DeleteLocalNetwork(ctx context.Context, id int64, opts *RequestOpts) error {
	path := fmt.Sprintf("/api/v2/local-networks/%d", id)

	// Only add query params if explicitly provided in opts (overrides provider defaults)
	if opts != nil && (opts.Region != "" || opts.ProjectTag != "") {
		params := url.Values{}
		if opts.Region != "" {
			params.Set("region", opts.Region)
		}
		if opts.ProjectTag != "" {
			params.Set("projectTag", opts.ProjectTag)
		}
		path = path + "?" + params.Encode()
	}

	if err := c.Do(ctx, http.MethodDelete, path, nil, nil, opts); err != nil {
		return err
	}
	return nil
}

// PublicIP represents a public IP resource.
type PublicIP struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	IP      string `json:"ip"`
	Mask    string `json:"mask"`
	Gateway string `json:"gateway"`
}

func (c *Client) GetPublicIPs(ctx context.Context, opts *RequestOpts) ([]PublicIP, error) {
	var ips []PublicIP
	if err := c.Do(ctx, http.MethodGet, "/api/v2/public-ips", nil, &ips, opts); err != nil {
		return nil, err
	}
	return ips, nil
}

func (c *Client) GetPublicIP(ctx context.Context, id int64, opts *RequestOpts) (*PublicIP, error) {
	var ip PublicIP
	path := fmt.Sprintf("/api/v2/public-ips/%d", id)
	if err := c.Do(ctx, http.MethodGet, path, nil, &ip, opts); err != nil {
		return nil, err
	}
	return &ip, nil
}

type CreatePublicIPRequest struct {
	Region     string `json:"region"`
	ProjectTag string `json:"projectTag"`
	Name       string `json:"name"`
}

func (c *Client) CreatePublicIP(ctx context.Context, req CreatePublicIPRequest) (*PublicIP, error) {
	if req.Region == "" {
		req.Region = c.Region
	}
	if req.ProjectTag == "" {
		req.ProjectTag = c.ProjectTag
	}

	var ip PublicIP
	if err := c.Do(ctx, http.MethodPost, "/api/v2/public-ips", req, &ip, nil); err != nil {
		return nil, err
	}
	return &ip, nil
}

type UpdatePublicIPRequest struct {
	Region     string `json:"region,omitempty"`
	ProjectTag string `json:"projectTag,omitempty"`
	Name       string `json:"name"`
}

func (c *Client) UpdatePublicIP(ctx context.Context, id int64, req UpdatePublicIPRequest) (*PublicIP, error) {
	if req.Region == "" {
		req.Region = c.Region
	}
	if req.ProjectTag == "" {
		req.ProjectTag = c.ProjectTag
	}

	path := fmt.Sprintf("/api/v2/public-ips/%d", id)
	var ip PublicIP
	if err := c.Do(ctx, http.MethodPut, path, req, &ip, nil); err != nil {
		return nil, err
	}
	return &ip, nil
}

func (c *Client) DeletePublicIP(ctx context.Context, id int64, opts *RequestOpts) error {
	path := fmt.Sprintf("/api/v2/public-ips/%d", id)

	// Only add query params if explicitly provided in opts (overrides provider defaults)
	if opts != nil && (opts.Region != "" || opts.ProjectTag != "") {
		params := url.Values{}
		if opts.Region != "" {
			params.Set("region", opts.Region)
		}
		if opts.ProjectTag != "" {
			params.Set("projectTag", opts.ProjectTag)
		}
		path = path + "?" + params.Encode()
	}

	if err := c.Do(ctx, http.MethodDelete, path, nil, nil, opts); err != nil {
		return err
	}
	return nil
}

// Vm represents a virtual machine.
type Vm struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Status         string `json:"status"`
	CPUCores       int64  `json:"cpuCores"`
	RAM            int64  `json:"ram"`
	DiskSize       int64  `json:"diskSize"`
	DiskType       string `json:"diskType"`
	PrivateIP      string `json:"privateIp"`
	PublicIP       string `json:"publicIp"`
	LocalNetworkID int64  `json:"localNetworkId"`
	Description    string `json:"description"`
}

// CreateVmRequest represents the request to create a VM.
type CreateVmRequest struct {
	Region         string  `json:"region,omitempty"`
	ProjectTag     string  `json:"projectTag,omitempty"`
	Name           string  `json:"name"`
	ImageID        int64   `json:"imageId"`
	CPUCores       int64   `json:"cpuCores"`
	RAM            int64   `json:"ram"`
	DiskSize       int64   `json:"diskSize"`
	DiskType       string  `json:"diskType"`
	LocalNetworkID int64   `json:"localNetworkId"`
	PrivateIP      *string `json:"privateIp,omitempty"`
	PublicIPID     *int64  `json:"publicIpId,omitempty"`
	Password       string  `json:"password"`
	SSHPublicKey   *string `json:"sshPublicKey,omitempty"`
	Description    *string `json:"description,omitempty"`
}

func (c *Client) GetVms(ctx context.Context, opts *RequestOpts) ([]Vm, error) {
	path := "/api/v2/vms"
	params := url.Values{}
	if opts != nil {
		if opts.Region != "" {
			params.Set("region", opts.Region)
		}
		if opts.ProjectTag != "" {
			params.Set("projectTag", opts.ProjectTag)
		}
	}
	if len(params) > 0 {
		path = path + "?" + params.Encode()
	}

	var vms []Vm
	if err := c.Do(ctx, http.MethodGet, path, nil, &vms, opts); err != nil {
		return nil, err
	}
	return vms, nil
}

func (c *Client) GetVm(ctx context.Context, id int64, opts *RequestOpts) (*Vm, error) {
	path := fmt.Sprintf("/api/v2/vms/%d", id)
	params := url.Values{}
	if opts != nil {
		if opts.Region != "" {
			params.Set("region", opts.Region)
		}
		if opts.ProjectTag != "" {
			params.Set("projectTag", opts.ProjectTag)
		}
	}
	if len(params) > 0 {
		path = path + "?" + params.Encode()
	}

	var vm Vm
	if err := c.Do(ctx, http.MethodGet, path, nil, &vm, opts); err != nil {
		return nil, err
	}
	return &vm, nil
}

// GetVmStatus returns a VM by ID, including VMs in ERROR status. Used for polling creation status.
func (c *Client) GetVmStatus(ctx context.Context, id int64, opts *RequestOpts) (*Vm, error) {
	path := fmt.Sprintf("/api/v2/vms/%d/status", id)
	params := url.Values{}
	if opts != nil {
		if opts.Region != "" {
			params.Set("region", opts.Region)
		}
		if opts.ProjectTag != "" {
			params.Set("projectTag", opts.ProjectTag)
		}
	}
	if len(params) > 0 {
		path = path + "?" + params.Encode()
	}

	var vm Vm
	if err := c.Do(ctx, http.MethodGet, path, nil, &vm, opts); err != nil {
		return nil, err
	}
	return &vm, nil
}

func (c *Client) CreateVm(ctx context.Context, req CreateVmRequest) (*Vm, error) {
	if req.Region == "" {
		req.Region = c.Region
	}
	if req.ProjectTag == "" {
		req.ProjectTag = c.ProjectTag
	}

	var vm Vm
	if err := c.Do(ctx, http.MethodPost, "/api/v2/vms", req, &vm, nil); err != nil {
		return nil, err
	}
	return &vm, nil
}

// AttachPublicIPRequest represents the request to attach a public IP to a VM.
type AttachPublicIPRequest struct {
	PublicIPID int64 `json:"publicIpId"`
}

func (c *Client) AttachPublicIP(ctx context.Context, vmID int64, req AttachPublicIPRequest, opts *RequestOpts) (*Vm, error) {
	path := fmt.Sprintf("/api/v2/vms/%d/public-ip", vmID)
	params := url.Values{}
	if opts != nil {
		if opts.Region != "" {
			params.Set("region", opts.Region)
		}
		if opts.ProjectTag != "" {
			params.Set("projectTag", opts.ProjectTag)
		}
	}
	if len(params) > 0 {
		path = path + "?" + params.Encode()
	}

	var vm Vm
	if err := c.Do(ctx, http.MethodPost, path, req, &vm, opts); err != nil {
		return nil, err
	}
	return &vm, nil
}

func (c *Client) DetachPublicIP(ctx context.Context, vmID int64, opts *RequestOpts) error {
	path := fmt.Sprintf("/api/v2/vms/%d/public-ip", vmID)
	params := url.Values{}
	if opts != nil {
		if opts.Region != "" {
			params.Set("region", opts.Region)
		}
		if opts.ProjectTag != "" {
			params.Set("projectTag", opts.ProjectTag)
		}
	}
	if len(params) > 0 {
		path = path + "?" + params.Encode()
	}

	if err := c.Do(ctx, http.MethodDelete, path, nil, nil, opts); err != nil {
		return err
	}
	return nil
}

// AttachVolumeRequest represents the request to attach a volume to a VM.
type AttachVolumeRequest struct {
	VolumeID int64 `json:"volumeId"`
}

func (c *Client) AttachVolume(ctx context.Context, vmID int64, req AttachVolumeRequest, opts *RequestOpts) (*Volume, error) {
	path := fmt.Sprintf("/api/v2/vms/%d/volumes", vmID)
	params := url.Values{}
	if opts != nil {
		if opts.Region != "" {
			params.Set("region", opts.Region)
		}
		if opts.ProjectTag != "" {
			params.Set("projectTag", opts.ProjectTag)
		}
	}
	if len(params) > 0 {
		path = path + "?" + params.Encode()
	}

	var volume Volume
	if err := c.Do(ctx, http.MethodPost, path, req, &volume, opts); err != nil {
		return nil, err
	}
	return &volume, nil
}

func (c *Client) DetachVolume(ctx context.Context, vmID int64, vmDiskID int64, opts *RequestOpts) error {
	path := fmt.Sprintf("/api/v2/vms/%d/volumes/%d", vmID, vmDiskID)
	params := url.Values{}
	if opts != nil {
		if opts.Region != "" {
			params.Set("region", opts.Region)
		}
		if opts.ProjectTag != "" {
			params.Set("projectTag", opts.ProjectTag)
		}
	}
	if len(params) > 0 {
		path = path + "?" + params.Encode()
	}

	if err := c.Do(ctx, http.MethodDelete, path, nil, nil, opts); err != nil {
		return err
	}
	return nil
}

type RenameVmRequest struct {
	Name string `json:"name"`
}

func (c *Client) RenameVm(ctx context.Context, id int64, req RenameVmRequest, opts *RequestOpts) error {
	path := fmt.Sprintf("/api/v2/vms/%d/name", id)
	params := url.Values{}
	if opts != nil {
		if opts.Region != "" {
			params.Set("region", opts.Region)
		}
		if opts.ProjectTag != "" {
			params.Set("projectTag", opts.ProjectTag)
		}
	}
	if len(params) > 0 {
		path = path + "?" + params.Encode()
	}
	if err := c.Do(ctx, http.MethodPatch, path, req, nil, opts); err != nil {
		return err
	}
	return nil
}

func (c *Client) DeleteVm(ctx context.Context, id int64, opts *RequestOpts) error {
	path := fmt.Sprintf("/api/v2/vms/%d", id)

	// Only add query params if explicitly provided in opts (overrides provider defaults)
	if opts != nil && (opts.Region != "" || opts.ProjectTag != "") {
		params := url.Values{}
		if opts.Region != "" {
			params.Set("region", opts.Region)
		}
		if opts.ProjectTag != "" {
			params.Set("projectTag", opts.ProjectTag)
		}
		path = path + "?" + params.Encode()
	}

	if err := c.Do(ctx, http.MethodDelete, path, nil, nil, opts); err != nil {
		return err
	}
	return nil
}
