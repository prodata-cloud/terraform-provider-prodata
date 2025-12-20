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
	Region       string
	ProjectID    int64
	httpClient   *http.Client
}

type Config struct {
	APIBaseURL   string
	APIKeyID     string
	APISecretKey string
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

func (c *Client) Do(ctx context.Context, method, path string, body, result any) error {
	var reqBody io.Reader
	var reqBodyStr string

	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reqBodyStr = string(b)
		reqBody = bytes.NewReader(b)
	}

	fullURL := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, fullURL, reqBody)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key-Id", c.apiKeyID)
	req.Header.Set("X-Api-Secret-Key", c.apiSecretKey)
	req.Header.Set("X-Region", c.Region)
	req.Header.Set("X-Project-Id", strconv.FormatInt(c.ProjectID, 10))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w\n%s", err, c.debugInfo(method, fullURL, req.Header, reqBodyStr, "", 0))
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w\n%s", err, c.debugInfo(method, fullURL, req.Header, reqBodyStr, "", resp.StatusCode))
	}

	var apiResp apiResponse[json.RawMessage]
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return fmt.Errorf("parse response: %w\n%s", err, c.debugInfo(method, fullURL, req.Header, reqBodyStr, string(respBody), resp.StatusCode))
	}

	if !apiResp.Success {
		return fmt.Errorf("api error: %s\n%s", formatAPIErrors(apiResp.Errors), c.debugInfo(method, fullURL, req.Header, reqBodyStr, string(respBody), resp.StatusCode))
	}

	if result != nil {
		if err := json.Unmarshal(apiResp.Data, result); err != nil {
			return fmt.Errorf("parse data: %w\n%s", err, c.debugInfo(method, fullURL, req.Header, reqBodyStr, string(respBody), resp.StatusCode))
		}
	}

	return nil
}

func (c *Client) debugInfo(method, url string, headers http.Header, reqBody, respBody string, statusCode int) string {
	var sb strings.Builder
	sb.WriteString("\n=== Debug ===\n")
	sb.WriteString(fmt.Sprintf("Method: %s\nURL: %s\n", method, url))
	sb.WriteString("Headers:\n")
	for k, v := range headers {
		val := strings.Join(v, ", ")
		if k == "X-Api-Secret-Key" && len(val) > 8 {
			val = val[:4] + "****" + val[len(val)-4:]
		}
		sb.WriteString(fmt.Sprintf("  %s: %s\n", k, val))
	}
	if reqBody != "" {
		sb.WriteString(fmt.Sprintf("Request Body: %s\n", reqBody))
	}
	if statusCode > 0 {
		sb.WriteString(fmt.Sprintf("Status: %d\n", statusCode))
	}
	if respBody != "" {
		sb.WriteString(fmt.Sprintf("Response: %s\n", respBody))
	}
	return sb.String()
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
	region := c.Region
	if q.Region != "" {
		region = q.Region
	}
	projectID := c.ProjectID
	if q.ProjectID != 0 {
		projectID = q.ProjectID
	}

	params := url.Values{}
	params.Set("region", region)
	params.Set("projectId", strconv.FormatInt(projectID, 10))

	if q.Slug != "" {
		params.Set("slug", q.Slug)
	} else if q.Name != "" {
		params.Set("name", q.Name)
	} else {
		return nil, fmt.Errorf("either slug or name is required")
	}

	var img Image
	err := c.Do(ctx, http.MethodGet, "/api/v2/image?"+params.Encode(), nil, &img)
	if err != nil {
		return nil, err
	}
	return &img, nil
}
