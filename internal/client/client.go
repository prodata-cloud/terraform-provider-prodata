package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	baseURL      string
	apiKeyID     string
	apiSecretKey string
	userAgent    string
	Region       string
	ProjectID    int64
	httpClient   *http.Client
}

type Config struct {
	APIBaseURL   string
	APIKeyID     string
	APISecretKey string
	UserAgent    string
	Region       string
	ProjectID    int64
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
		ProjectID:    cfg.ProjectID,
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
	Region    string
	ProjectID int64
}

func (c *Client) Do(ctx context.Context, method, path string, body, result any, opts *RequestOpts) error {
	var reqBody io.Reader
	var reqBodyBytes []byte

	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			log.Printf("[ERROR] Failed to marshal request body: %v", err)
			log.Printf("[ERROR] Request: %s %s", method, path)
			log.Printf("[ERROR] Body: %+v", body)
			return fmt.Errorf("marshal request: %w", err)
		}
		reqBodyBytes = b
		reqBody = bytes.NewReader(b)
	}

	fullURL := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, fullURL, reqBody)
	if err != nil {
		log.Printf("[ERROR] Failed to create HTTP request: %v", err)
		log.Printf("[ERROR] Request: %s %s", method, fullURL)
		return fmt.Errorf("create request: %w", err)
	}

	// Determine region and project: use per-request opts if provided, else client defaults.
	region := c.Region
	projectID := c.ProjectID
	if opts != nil {
		if opts.Region != "" {
			region = opts.Region
		}
		if opts.ProjectID != 0 {
			projectID = opts.ProjectID
		}
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("X-Api-Key-Id", c.apiKeyID)
	req.Header.Set("X-Api-Secret-Key", c.apiSecretKey)
	req.Header.Set("X-Region", region)
	req.Header.Set("X-Project-Id", strconv.FormatInt(projectID, 10))

	log.Printf("[DEBUG] API Request: %s %s", method, fullURL)
	log.Printf("[DEBUG] Headers: X-Region=%s, X-Project-Id=%d", region, projectID)
	if len(reqBodyBytes) > 0 {
		log.Printf("[DEBUG] Request Body: %s", string(reqBodyBytes))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Printf("[ERROR] HTTP request failed: %v", err)
		log.Printf("[ERROR] Request: %s %s", method, fullURL)
		log.Printf("[ERROR] Headers: X-Region=%s, X-Project-Id=%d", region, projectID)
		if len(reqBodyBytes) > 0 {
			log.Printf("[ERROR] Request Body: %s", string(reqBodyBytes))
		}
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	log.Printf("[DEBUG] Response Status: %d %s", resp.StatusCode, resp.Status)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[ERROR] Failed to read response body: %v", err)
		log.Printf("[ERROR] Request: %s %s", method, fullURL)
		log.Printf("[ERROR] Response Status: %d", resp.StatusCode)
		return fmt.Errorf("read response: %w", err)
	}

	log.Printf("[DEBUG] Response Body: %s", string(respBody))

	var apiResp apiResponse[json.RawMessage]
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		log.Printf("[ERROR] Failed to parse response JSON: %v", err)
		log.Printf("[ERROR] Request: %s %s", method, fullURL)
		log.Printf("[ERROR] Response Status: %d", resp.StatusCode)
		log.Printf("[ERROR] Response Body: %s", string(respBody))
		return fmt.Errorf("parse response: %w", err)
	}

	if !apiResp.Success {
		log.Printf("[ERROR] API returned error response")
		log.Printf("[ERROR] Request: %s %s", method, fullURL)
		log.Printf("[ERROR] Headers: X-Region=%s, X-Project-Id=%d", region, projectID)
		if len(reqBodyBytes) > 0 {
			log.Printf("[ERROR] Request Body: %s", string(reqBodyBytes))
		}
		log.Printf("[ERROR] Response Status: %d", resp.StatusCode)
		log.Printf("[ERROR] Response Body: %s", string(respBody))
		log.Printf("[ERROR] API Errors: %s", formatAPIErrors(apiResp.Errors))
		return fmt.Errorf("api error: %s", formatAPIErrors(apiResp.Errors))
	}

	if result != nil {
		if err := json.Unmarshal(apiResp.Data, result); err != nil {
			log.Printf("[ERROR] Failed to parse response data: %v", err)
			log.Printf("[ERROR] Request: %s %s", method, fullURL)
			log.Printf("[ERROR] Response Data: %s", string(apiResp.Data))
			return fmt.Errorf("parse data: %w", err)
		}
	}

	return nil
}

func formatAPIErrors(errs []apiError) string {
	if len(errs) == 0 {
		return "unknown error"
	}
	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = fmt.Sprintf("[%d] %s", e.Code, e.Message)
	}
	return strings.Join(msgs, "; ")
}

type Image struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	IsCustom bool   `json:"isCustom"`
}

type ImageQuery struct {
	Slug      string
	Name      string
	Region    string
	ProjectID int64
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
		Region:    q.Region,
		ProjectID: q.ProjectID,
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
	Region    string `json:"region"`
	ProjectID int64  `json:"projectId"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Size      int64  `json:"size"`
}

func (c *Client) CreateVolume(ctx context.Context, req CreateVolumeRequest) (*Volume, error) {
	if req.Region == "" {
		req.Region = c.Region
	}
	if req.ProjectID == 0 {
		req.ProjectID = c.ProjectID
	}

	var volume Volume
	if err := c.Do(ctx, http.MethodPost, "/api/v2/volumes", req, &volume, nil); err != nil {
		return nil, err
	}
	return &volume, nil
}

type UpdateVolumeRequest struct {
	Region    string `json:"region,omitempty"`
	ProjectID int64  `json:"projectId,omitempty"`
	Name      string `json:"name"`
}

func (c *Client) UpdateVolume(ctx context.Context, id int64, req UpdateVolumeRequest) (*Volume, error) {
	if req.Region == "" {
		req.Region = c.Region
	}
	if req.ProjectID == 0 {
		req.ProjectID = c.ProjectID
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
	if opts != nil && (opts.Region != "" || opts.ProjectID != 0) {
		params := url.Values{}
		if opts.Region != "" {
			params.Set("region", opts.Region)
		}
		if opts.ProjectID != 0 {
			params.Set("projectId", strconv.FormatInt(opts.ProjectID, 10))
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
	Region    string `json:"region"`
	ProjectID int64  `json:"projectId"`
	Name      string `json:"name"`
	CIDR      string `json:"cidr"`
	Gateway   string `json:"gateway"`
}

func (c *Client) CreateLocalNetwork(ctx context.Context, req CreateLocalNetworkRequest) (*LocalNetwork, error) {
	if req.Region == "" {
		req.Region = c.Region
	}
	if req.ProjectID == 0 {
		req.ProjectID = c.ProjectID
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
	Region    string `json:"region,omitempty"`
	ProjectID int64  `json:"projectId,omitempty"`
	Name      string `json:"name"`
}

func (c *Client) UpdateLocalNetwork(ctx context.Context, id int64, req UpdateLocalNetworkRequest) (*LocalNetwork, error) {
	if req.Region == "" {
		req.Region = c.Region
	}
	if req.ProjectID == 0 {
		req.ProjectID = c.ProjectID
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
	if opts != nil && (opts.Region != "" || opts.ProjectID != 0) {
		params := url.Values{}
		if opts.Region != "" {
			params.Set("region", opts.Region)
		}
		if opts.ProjectID != 0 {
			params.Set("projectId", strconv.FormatInt(opts.ProjectID, 10))
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
	Region    string `json:"region"`
	ProjectID int64  `json:"projectId"`
	Name      string `json:"name"`
}

func (c *Client) CreatePublicIP(ctx context.Context, req CreatePublicIPRequest) (*PublicIP, error) {
	if req.Region == "" {
		req.Region = c.Region
	}
	if req.ProjectID == 0 {
		req.ProjectID = c.ProjectID
	}

	var ip PublicIP
	if err := c.Do(ctx, http.MethodPost, "/api/v2/public-ips", req, &ip, nil); err != nil {
		return nil, err
	}
	return &ip, nil
}

type UpdatePublicIPRequest struct {
	Region    string `json:"region,omitempty"`
	ProjectID int64  `json:"projectId,omitempty"`
	Name      string `json:"name"`
}

func (c *Client) UpdatePublicIP(ctx context.Context, id int64, req UpdatePublicIPRequest) (*PublicIP, error) {
	if req.Region == "" {
		req.Region = c.Region
	}
	if req.ProjectID == 0 {
		req.ProjectID = c.ProjectID
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
	if opts != nil && (opts.Region != "" || opts.ProjectID != 0) {
		params := url.Values{}
		if opts.Region != "" {
			params.Set("region", opts.Region)
		}
		if opts.ProjectID != 0 {
			params.Set("projectId", strconv.FormatInt(opts.ProjectID, 10))
		}
		path = path + "?" + params.Encode()
	}

	if err := c.Do(ctx, http.MethodDelete, path, nil, nil, opts); err != nil {
		return err
	}
	return nil
}

