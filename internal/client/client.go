package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

const (
	// rateLimitMaxRetries bounds how many times Do retries an HTTP 429 response.
	// Sized so a bulk apply survives a sustained upstream rate-limit window:
	// with a ~10s Retry-After this is roughly 100s of retry budget per request.
	rateLimitMaxRetries = 10
	// rateLimitBaseDelay is the first backoff interval; it doubles each attempt.
	rateLimitBaseDelay = 2 * time.Second
	// rateLimitMaxDelay caps a single backoff wait.
	rateLimitMaxDelay = 60 * time.Second
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
		httpClient:   &http.Client{Timeout: 60 * time.Second},
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

// doRequest performs an HTTP request with the standard auth + region/project headers
// and transparent HTTP 429 retry, returning the raw status code and response body.
// It is the shared transport for both the V2 envelope path (Do) and the V1 envelope
// path (legacy load-balancer endpoints, parsed via parseV1Response).
func (c *Client) doRequest(ctx context.Context, method, path string, body any, opts *RequestOpts) (int, []byte, error) {
	var bodyBytes []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("marshal request: %w", err)
		}
		bodyBytes = b
	}

	fullURL := c.baseURL + path

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

	// Edge rate limiting (HTTP 429 — e.g. Cloudflare error 1015) rejects the
	// request before it reaches the API, so retrying is safe even for POST/DELETE:
	// no work was performed. Bulk applies (many parallel resources) trip per-IP
	// rate limits; retry transparently with backoff instead of failing the apply.
	for attempt := 0; ; attempt++ {
		var reqBody io.Reader
		if bodyBytes != nil {
			reqBody = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, method, fullURL, reqBody)
		if err != nil {
			return 0, nil, fmt.Errorf("create request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("X-API-KEY", c.apiKeyID)
		req.Header.Set("X-API-SECRET", c.apiSecretKey)
		req.Header.Set("X-Region", region)
		req.Header.Set("X-Project-Tag", projectTag)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return 0, nil, fmt.Errorf("request failed: %w", err)
		}

		respBody, readErr := io.ReadAll(resp.Body)
		respHeader := resp.Header
		statusCode := resp.StatusCode
		resp.Body.Close()
		if readErr != nil {
			return 0, nil, fmt.Errorf("read response: %w", readErr)
		}

		if statusCode == http.StatusTooManyRequests && attempt < rateLimitMaxRetries {
			wait := retryAfterDelay(respHeader, attempt)
			tflog.Warn(ctx, "rate limited (HTTP 429) — backing off before retry", map[string]any{
				"method":      method,
				"path":        path,
				"attempt":     attempt + 1,
				"max_retries": rateLimitMaxRetries,
				"retry_in":    wait.String(),
			})
			select {
			case <-ctx.Done():
				return 0, nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}

		return statusCode, respBody, nil
	}
}

// Do issues a request to a V2 API endpoint and decodes the {success,data,errors}
// envelope into result.
func (c *Client) Do(ctx context.Context, method, path string, body, result any, opts *RequestOpts) error {
	statusCode, respBody, err := c.doRequest(ctx, method, path, body, opts)
	if err != nil {
		return err
	}
	return parseResponse(statusCode, respBody, result)
}

// parseResponse turns an HTTP response into either nil, a populated result, or an *APIError.
func parseResponse(statusCode int, respBody []byte, result any) error {
	// Always try to parse as structured API response.
	var apiResp apiResponse[json.RawMessage]
	parsed := json.Unmarshal(respBody, &apiResp) == nil

	isHTTPError := statusCode < 200 || statusCode >= 300
	isAPIFailure := parsed && !apiResp.Success

	if isHTTPError || isAPIFailure {
		apiErr := &APIError{
			StatusCode: statusCode,
			RawBody:    string(respBody),
		}
		if parsed && len(apiResp.Errors) > 0 {
			msgs := make([]string, len(apiResp.Errors))
			for i, e := range apiResp.Errors {
				apiErr.Codes = append(apiErr.Codes, e.Code)
				msgs[i] = e.Message
			}
			apiErr.Message = strings.Join(msgs, "; ")
		} else {
			apiErr.Message = string(respBody)
		}
		return apiErr
	}

	if result != nil {
		if !parsed {
			return fmt.Errorf("unexpected response format (status %d): %s", statusCode, string(respBody))
		}
		if err := json.Unmarshal(apiResp.Data, result); err != nil {
			return fmt.Errorf("parse data: %w", err)
		}
	}

	return nil
}

// retryAfterDelay computes how long to wait before retrying an HTTP 429 response.
// It honors the Retry-After header (delay-seconds or HTTP-date form) when present,
// otherwise falls back to exponential backoff (2s, 4s, 8s, ...). The result is
// jittered (±25%) to avoid a thundering herd when many parallel resources are
// rate-limited at once, and each wait is capped at rateLimitMaxDelay.
func retryAfterDelay(h http.Header, attempt int) time.Duration {
	delay := time.Duration(0)
	fromHeader := false
	if ra := strings.TrimSpace(h.Get("Retry-After")); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil && secs >= 0 {
			delay, fromHeader = time.Duration(secs)*time.Second, true
		} else if t, err := http.ParseTime(ra); err == nil {
			if d := time.Until(t); d > 0 {
				delay = d
			}
			fromHeader = true
		}
	}
	if !fromHeader {
		// Exponential backoff: rateLimitBaseDelay doubles each attempt.
		delay = rateLimitBaseDelay << attempt
		if delay <= 0 { // guard against shift overflow
			delay = rateLimitMaxDelay
		}
	}
	if delay > rateLimitMaxDelay {
		delay = rateLimitMaxDelay
	}
	if delay <= 0 {
		return 0
	}
	// ±25% jitter, then clamp so rateLimitMaxDelay stays a hard ceiling.
	jitter := time.Duration(rand.Int63n(int64(delay)/2+1)) - delay/4
	delay += jitter
	if delay < 0 {
		delay = 0
	} else if delay > rateLimitMaxDelay {
		delay = rateLimitMaxDelay
	}
	return delay
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

// VmDisk represents an attached volume (VmDisk) on a VM.
type VmDisk struct {
	ID         int64  `json:"id"`
	UserDiskID *int64 `json:"userDiskId"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Size       int64  `json:"size"`
	BootDisk   bool   `json:"bootDisk"`
}

func (c *Client) GetVmVolumes(ctx context.Context, vmID int64, opts *RequestOpts) ([]VmDisk, error) {
	var disks []VmDisk
	path := fmt.Sprintf("/api/v2/vms/%d/volumes", vmID)
	if err := c.Do(ctx, http.MethodGet, path, nil, &disks, opts); err != nil {
		return nil, err
	}
	return disks, nil
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
	path := fmt.Sprintf("/api/v2/local-networks/%d", id)
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

	var network LocalNetwork
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
	path := fmt.Sprintf("/api/v2/public-ips/%d", id)
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

	var ip PublicIP
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
	PublicIPID     int64  `json:"publicIpId"`
	LocalNetworkID int64  `json:"localNetworkId"`
	ImageID        int64  `json:"imageId"`
	ImageName      string `json:"imageName"`
	ImageSlug      string `json:"imageSlug"`
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

type UpdateVmResourcesRequest struct {
	CPUCores int64 `json:"cpuCores"`
	RAM      int64 `json:"ram"`
}

func (c *Client) UpdateVmResources(ctx context.Context, id int64, req UpdateVmResourcesRequest, opts *RequestOpts) error {
	path := fmt.Sprintf("/api/v2/vms/%d/resources", id)
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

type UpdateVmDiskRequest struct {
	DiskSize *int64  `json:"diskSize,omitempty"`
	DiskType *string `json:"diskType,omitempty"`
}

func (c *Client) UpdateVmDisk(ctx context.Context, id int64, req UpdateVmDiskRequest, opts *RequestOpts) error {
	path := fmt.Sprintf("/api/v2/vms/%d/disk", id)
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

func (c *Client) StopVm(ctx context.Context, id int64, opts *RequestOpts) error {
	path := fmt.Sprintf("/api/v2/vms/%d/stop", id)
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
	if err := c.Do(ctx, http.MethodPost, path, nil, nil, opts); err != nil {
		return err
	}
	return nil
}

func (c *Client) StartVm(ctx context.Context, id int64, opts *RequestOpts) error {
	path := fmt.Sprintf("/api/v2/vms/%d/start", id)
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
	if err := c.Do(ctx, http.MethodPost, path, nil, nil, opts); err != nil {
		return err
	}
	return nil
}

// WaitForVmStatus polls the VM until it reaches targetStatus or timeout.
// Tolerates up to 3 consecutive transient errors during polling.
func (c *Client) WaitForVmStatus(ctx context.Context, vmID int64, targetStatus string, timeout time.Duration, opts *RequestOpts) error {
	const (
		pollInterval       = 5 * time.Second
		maxConsecutiveErrs = 3
	)

	deadline := time.Now().Add(timeout)
	consecutiveErrs := 0

	for {
		vm, err := c.GetVmStatus(ctx, vmID, opts)
		if err != nil {
			consecutiveErrs++
			if consecutiveErrs >= maxConsecutiveErrs {
				return fmt.Errorf("polling VM %d: %w (after %d consecutive failures)", vmID, err, consecutiveErrs)
			}
		} else {
			consecutiveErrs = 0
			if vm.Status == targetStatus {
				return nil
			}
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for VM %d to reach %s", vmID, targetStatus)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}
