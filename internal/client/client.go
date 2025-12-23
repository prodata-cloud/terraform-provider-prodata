package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	var apiResp apiResponse[json.RawMessage]
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	if !apiResp.Success {
		return fmt.Errorf("api error: %s", formatAPIErrors(apiResp.Errors))
	}

	if result != nil {
		if err := json.Unmarshal(apiResp.Data, result); err != nil {
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
