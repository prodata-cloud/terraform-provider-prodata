package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Bucket is the panel-main S3 bucket DTO. `size` and `objectCount` are populated
// only on GET; CREATE/LIST may omit or zero them depending on the endpoint.
type Bucket struct {
	Name         string `json:"name"`
	CreationDate string `json:"creationDate,omitempty"`
	Size         *int64 `json:"size,omitempty"`
	ObjectCount  *int64 `json:"objectCount,omitempty"`
}

// VersioningConfiguration mirrors the panel DTO. Status is the Java enum NAME:
// "ENABLED" or "SUSPENDED". Never send "DISABLED" — omit the wrapper instead.
type VersioningConfiguration struct {
	Status string `json:"status"`
}

// ObjectLockConfiguration mirrors the panel DTO. ObjectLockEnabled is the
// Java enum NAME ("ENABLED"); Rule is null/omitted for the MVP.
type ObjectLockConfiguration struct {
	ObjectLockEnabled string          `json:"objectLockEnabled,omitempty"`
	Rule              *ObjectLockRule `json:"rule,omitempty"`
}

type ObjectLockRule struct {
	DefaultRetention *DefaultRetention `json:"defaultRetention,omitempty"`
}

type DefaultRetention struct {
	Mode  string `json:"mode,omitempty"`
	Days  *int64 `json:"days,omitempty"`
	Years *int64 `json:"years,omitempty"`
}

// CreateBucketRequest mirrors panel-main `dto/CreateBucketRequest.java`.
// Field MUST be `bucketKey` (not `name`) — server-side @NotBlank fails otherwise.
// Acl is the Java enum NAME: "PRIVATE" / "PUBLIC_READ" / "PUBLIC_READ_WRITE".
type CreateBucketRequest struct {
	BucketKey                  string                   `json:"bucketKey"`
	VersioningConfiguration    *VersioningConfiguration `json:"versioningConfiguration,omitempty"`
	ObjectLockEnabledForBucket *bool                    `json:"objectLockEnabledForBucket,omitempty"`
	Acl                        string                   `json:"acl,omitempty"`
}

type PutBucketVersioningRequest struct {
	VersioningConfiguration *VersioningConfiguration `json:"versioningConfiguration"`
}

type PutObjectLockConfigurationRequest struct {
	ObjectLockConfiguration *ObjectLockConfiguration `json:"objectLockConfiguration"`
}

type PutBucketAclRequest struct {
	Acl                 string               `json:"acl,omitempty"`
	AccessControlPolicy *AccessControlPolicy `json:"accessControlPolicy,omitempty"`
}

// AccessControlPolicy / Owner / Grant / Grantee mirror panel `dto/acp/*`.
// Exposed for callers that need policy-only ACL; resource MVP uses canned Acl only.
type AccessControlPolicy struct {
	Owner  *Owner  `json:"owner,omitempty"`
	Grants []Grant `json:"grants,omitempty"`
}

type Owner struct {
	ID          string `json:"id,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
}

type Grant struct {
	Grantee    *Grantee `json:"grantee,omitempty"`
	Permission string   `json:"permission,omitempty"`
}

type Grantee struct {
	Type         string `json:"type,omitempty"`
	ID           string `json:"id,omitempty"`
	DisplayName  string `json:"displayName,omitempty"`
	URI          string `json:"uri,omitempty"`
	EmailAddress string `json:"emailAddress,omitempty"`
}

type GetBucketAclResponse struct {
	Owner  *Owner  `json:"owner,omitempty"`
	Grants []Grant `json:"grants,omitempty"`
}

// listBucketsPage is the panel `ListBucketsResult` DTO returned by GET /buckets.
type listBucketsPage struct {
	Buckets           []Bucket `json:"buckets"`
	ContinuationToken string   `json:"continuationToken,omitempty"`
}

// ---- bucket paths ----

const bucketsPath = "/storage/api/v1/buckets"

func bucketPath(name string) string {
	return bucketsPath + "/" + url.PathEscape(name)
}

// ---- bucket methods ----

// CreateBucket sends POST /buckets. Panel returns 201 with empty body + Location header.
func (c *Client) CreateBucket(ctx context.Context, req CreateBucketRequest, opts *RequestOpts) error {
	return c.Do(ctx, http.MethodPost, bucketsPath, req, nil, opts)
}

// GetBucket fetches a single bucket. Returns *APIError with code 628 if not found,
// or code 712 if the bucket exists but belongs to another project (do NOT silently
// drop state — must surface as a hard error to the caller).
func (c *Client) GetBucket(ctx context.Context, name string, opts *RequestOpts) (*Bucket, error) {
	var b Bucket
	if err := c.Do(ctx, http.MethodGet, bucketPath(name), nil, &b, opts); err != nil {
		return nil, err
	}
	return &b, nil
}

// ListBuckets follows pagination until the server stops returning a continuationToken.
// pageSize is forwarded as the `maxBuckets` query param when > 0.
func (c *Client) ListBuckets(ctx context.Context, pageSize int, opts *RequestOpts) ([]Bucket, error) {
	var out []Bucket
	var token string
	for {
		params := url.Values{}
		if pageSize > 0 {
			params.Set("maxBuckets", strconv.Itoa(pageSize))
		}
		if token != "" {
			params.Set("continuationToken", token)
		}
		path := bucketsPath
		if len(params) > 0 {
			path = path + "?" + params.Encode()
		}

		var page listBucketsPage
		if err := c.Do(ctx, http.MethodGet, path, nil, &page, opts); err != nil {
			return nil, err
		}
		out = append(out, page.Buckets...)
		if page.ContinuationToken == "" {
			return out, nil
		}
		token = page.ContinuationToken
	}
}

// PutBucketAcl sends PUT /buckets/{name}/acl. Server returns 204.
func (c *Client) PutBucketAcl(ctx context.Context, name string, req PutBucketAclRequest, opts *RequestOpts) error {
	return c.Do(ctx, http.MethodPut, bucketPath(name)+"/acl", req, nil, opts)
}

func (c *Client) GetBucketAcl(ctx context.Context, name string, opts *RequestOpts) (*GetBucketAclResponse, error) {
	var resp GetBucketAclResponse
	if err := c.Do(ctx, http.MethodGet, bucketPath(name)+"/acl", nil, &resp, opts); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) PutBucketVersioning(ctx context.Context, name string, req PutBucketVersioningRequest, opts *RequestOpts) error {
	return c.Do(ctx, http.MethodPut, bucketPath(name)+"/versioning", req, nil, opts)
}

// GetBucketVersioning returns nil if the server has never been configured (panel
// returns `versioningConfiguration: null` for that case).
func (c *Client) GetBucketVersioning(ctx context.Context, name string, opts *RequestOpts) (*VersioningConfiguration, error) {
	var resp struct {
		VersioningConfiguration *VersioningConfiguration `json:"versioningConfiguration"`
	}
	if err := c.Do(ctx, http.MethodGet, bucketPath(name)+"/versioning", nil, &resp, opts); err != nil {
		return nil, err
	}
	return resp.VersioningConfiguration, nil
}

func (c *Client) PutObjectLockConfiguration(ctx context.Context, name string, req PutObjectLockConfigurationRequest, opts *RequestOpts) error {
	return c.Do(ctx, http.MethodPut, bucketPath(name)+"/object-locking", req, nil, opts)
}

// GetObjectLockConfiguration returns nil if no object-lock has ever been set
// (panel A6 maps ObjectLockConfigurationNotFoundError → `objectLockConfiguration: null`).
func (c *Client) GetObjectLockConfiguration(ctx context.Context, name string, opts *RequestOpts) (*ObjectLockConfiguration, error) {
	var resp struct {
		ObjectLockConfiguration *ObjectLockConfiguration `json:"objectLockConfiguration"`
	}
	if err := c.Do(ctx, http.MethodGet, bucketPath(name)+"/object-locking", nil, &resp, opts); err != nil {
		return nil, err
	}
	return resp.ObjectLockConfiguration, nil
}

// DeleteBucket sends DELETE /buckets/{name}?forceDestroy=true|false.
// forceDestroy=true wipes objects/versions/multipart first; =false returns 409
// with code 626 ("bucket not empty") on a non-empty bucket. 628 means already gone.
func (c *Client) DeleteBucket(ctx context.Context, name string, forceDestroy bool, opts *RequestOpts) error {
	path := fmt.Sprintf("%s?forceDestroy=%t", bucketPath(name), forceDestroy)
	return c.Do(ctx, http.MethodDelete, path, nil, nil, opts)
}
