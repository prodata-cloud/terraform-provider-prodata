package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// captureReq inspects the incoming HTTP request and stores its method, path,
// raw query, and JSON-decoded body for assertions.
type captureReq struct {
	method   string
	path     string
	rawQuery string
	body     map[string]any
}

func newCapturingServer(t *testing.T, status int, body string) (*httptest.Server, *captureReq) {
	t.Helper()
	cap := &captureReq{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.rawQuery = r.URL.RawQuery
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &cap.body)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if status > 0 {
			w.WriteHeader(status)
		}
		if body != "" {
			_, _ = w.Write([]byte(body))
		}
	}))
	return srv, cap
}

// ---- 1. CreateBucket happy path: 201 + empty body returns no error ----

func TestCreateBucket_HappyPath(t *testing.T) {
	srv, cap := newCapturingServer(t, http.StatusCreated, "")
	defer srv.Close()

	c := newTestClient(t, srv)
	err := c.CreateBucket(context.Background(), CreateBucketRequest{
		BucketKey: "my-bucket",
		Acl:       "PRIVATE",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cap.method != http.MethodPost {
		t.Errorf("method = %q, want POST", cap.method)
	}
	if cap.path != "/panel-main/storage/api/v1/buckets" {
		t.Errorf("path = %q", cap.path)
	}
}

// ---- 2. CreateBucket sends Java enum NAMES on the wire (PRIVATE, ENABLED) ----

func TestCreateBucket_SendsEnumNames(t *testing.T) {
	srv, cap := newCapturingServer(t, http.StatusCreated, "")
	defer srv.Close()

	c := newTestClient(t, srv)
	objLock := true
	err := c.CreateBucket(context.Background(), CreateBucketRequest{
		BucketKey:                  "my-bucket",
		Acl:                        "PUBLIC_READ",
		VersioningConfiguration:    &VersioningConfiguration{Status: "ENABLED"},
		ObjectLockEnabledForBucket: &objLock,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := cap.body["bucketKey"]; got != "my-bucket" {
		t.Errorf("bucketKey = %v, want %q", got, "my-bucket")
	}
	if got := cap.body["acl"]; got != "PUBLIC_READ" {
		t.Errorf("acl = %v, want %q (Java enum NAME, not wire value)", got, "PUBLIC_READ")
	}
	vc, _ := cap.body["versioningConfiguration"].(map[string]any)
	if vc == nil || vc["status"] != "ENABLED" {
		t.Errorf("versioningConfiguration.status = %v, want %q", cap.body["versioningConfiguration"], "ENABLED")
	}
	if got := cap.body["objectLockEnabledForBucket"]; got != true {
		t.Errorf("objectLockEnabledForBucket = %v, want true", got)
	}
	if _, present := cap.body["name"]; present {
		t.Errorf("payload contains forbidden field `name` — must use `bucketKey`: %v", cap.body)
	}
}

// ---- 3. CreateBucket omits versioningConfiguration when nil (object_lock=false case) ----

func TestCreateBucket_OmitsNilVersioning(t *testing.T) {
	srv, cap := newCapturingServer(t, http.StatusCreated, "")
	defer srv.Close()

	c := newTestClient(t, srv)
	err := c.CreateBucket(context.Background(), CreateBucketRequest{
		BucketKey: "my-bucket",
		Acl:       "PRIVATE",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, present := cap.body["versioningConfiguration"]; present {
		t.Errorf("expected versioningConfiguration to be omitted when nil, got: %v", cap.body)
	}
	if _, present := cap.body["objectLockEnabledForBucket"]; present {
		t.Errorf("expected objectLockEnabledForBucket to be omitted when nil, got: %v", cap.body)
	}
}

// ---- 4. CreateBucket bucket-already-exists (626) surfaces as APIError ----

func TestCreateBucket_AlreadyExists(t *testing.T) {
	body := `{"success":false,"data":null,"errors":[{"code":626,"message":"bucket already exists"}]}`
	srv, _ := newCapturingServer(t, http.StatusConflict, body)
	defer srv.Close()

	c := newTestClient(t, srv)
	err := c.CreateBucket(context.Background(), CreateBucketRequest{BucketKey: "x"}, nil)
	if !IsAPIError(err, 626) {
		t.Fatalf("expected APIError code 626, got: %v", err)
	}
}

// ---- 5. GetBucket happy path returns Bucket ----

func TestGetBucket_HappyPath(t *testing.T) {
	body := `{"success":true,"data":{"name":"my-bucket","creationDate":"2026-01-15T10:30:00Z","size":1024,"objectCount":3}}`
	srv, cap := newCapturingServer(t, http.StatusOK, body)
	defer srv.Close()

	c := newTestClient(t, srv)
	b, err := c.GetBucket(context.Background(), "my-bucket", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.Name != "my-bucket" || b.CreationDate == "" || b.Size == nil || *b.Size != 1024 {
		t.Errorf("got %+v", b)
	}
	if cap.path != "/panel-main/storage/api/v1/buckets/my-bucket" {
		t.Errorf("path = %q", cap.path)
	}
}

// ---- 6. GetBucket 628 → IsNotFound true ----

func TestGetBucket_NotFound(t *testing.T) {
	body := `{"success":false,"data":null,"errors":[{"code":628,"message":"bucket not found"}]}`
	srv, _ := newCapturingServer(t, http.StatusBadRequest, body)
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.GetBucket(context.Background(), "missing", nil)
	if !IsNotFound(err) {
		t.Fatalf("expected IsNotFound=true for code 628, got: %v", err)
	}
}

// ---- 7. GetBucket cross-project (712) is APIError; IsNotFound = FALSE ----

func TestGetBucket_CrossProject_NotNotFound(t *testing.T) {
	body := `{"success":false,"data":null,"errors":[{"code":712,"message":"bucket belongs to another project"}]}`
	srv, _ := newCapturingServer(t, http.StatusForbidden, body)
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.GetBucket(context.Background(), "someones-bucket", nil)
	if !IsAPIError(err, 712) {
		t.Fatalf("expected APIError code 712, got: %v", err)
	}
	if IsNotFound(err) {
		t.Fatal("712 must NOT map to IsNotFound — silently dropping someone else's bucket from state would be unsafe")
	}
}

// ---- 8. ListBuckets single page (no continuation token) ----

func TestListBuckets_SinglePage(t *testing.T) {
	body := `{"success":true,"data":{"buckets":[{"name":"b1"},{"name":"b2"}],"continuationToken":null}}`
	srv, _ := newCapturingServer(t, http.StatusOK, body)
	defer srv.Close()

	c := newTestClient(t, srv)
	out, err := c.ListBuckets(context.Background(), 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 || out[0].Name != "b1" || out[1].Name != "b2" {
		t.Errorf("got %+v", out)
	}
}

// ---- 9. ListBuckets paginated: follows continuationToken until empty ----

func TestListBuckets_Paginated(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		switch calls {
		case 1:
			if got := r.URL.Query().Get("continuationToken"); got != "" {
				t.Errorf("page 1 should not send continuationToken, got %q", got)
			}
			_, _ = w.Write([]byte(`{"success":true,"data":{"buckets":[{"name":"b1"}],"continuationToken":"tok-1"}}`))
		case 2:
			if got := r.URL.Query().Get("continuationToken"); got != "tok-1" {
				t.Errorf("page 2 continuationToken = %q, want %q", got, "tok-1")
			}
			_, _ = w.Write([]byte(`{"success":true,"data":{"buckets":[{"name":"b2"},{"name":"b3"}],"continuationToken":"tok-2"}}`))
		case 3:
			_, _ = w.Write([]byte(`{"success":true,"data":{"buckets":[{"name":"b4"}],"continuationToken":""}}`))
		default:
			t.Fatalf("unexpected 4th request")
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	out, err := c.ListBuckets(context.Background(), 100, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 paginated calls, got %d", calls)
	}
	if len(out) != 4 {
		t.Errorf("expected 4 buckets across pages, got %d: %+v", len(out), out)
	}
	want := []string{"b1", "b2", "b3", "b4"}
	for i, n := range want {
		if out[i].Name != n {
			t.Errorf("out[%d].Name = %q, want %q", i, out[i].Name, n)
		}
	}
}

// ---- 10. ListBuckets empty result returns empty slice (not error) ----

func TestListBuckets_Empty(t *testing.T) {
	srv, _ := newCapturingServer(t, http.StatusOK, `{"success":true,"data":{"buckets":[],"continuationToken":null}}`)
	defer srv.Close()

	c := newTestClient(t, srv)
	out, err := c.ListBuckets(context.Background(), 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty slice, got %+v", out)
	}
}

// ---- 11. PutBucketAcl — sends correct path + canned ACL on the wire ----

func TestPutBucketAcl_HappyPath(t *testing.T) {
	srv, cap := newCapturingServer(t, http.StatusNoContent, "")
	defer srv.Close()

	c := newTestClient(t, srv)
	err := c.PutBucketAcl(context.Background(), "my-bucket", PutBucketAclRequest{Acl: "PUBLIC_READ_WRITE"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cap.method != http.MethodPut {
		t.Errorf("method = %q, want PUT", cap.method)
	}
	if cap.path != "/panel-main/storage/api/v1/buckets/my-bucket/acl" {
		t.Errorf("path = %q", cap.path)
	}
	if cap.body["acl"] != "PUBLIC_READ_WRITE" {
		t.Errorf("acl = %v, want PUBLIC_READ_WRITE", cap.body["acl"])
	}
	if _, present := cap.body["accessControlPolicy"]; present {
		t.Errorf("expected accessControlPolicy to be omitted when nil, got %v", cap.body)
	}
}

// ---- 12. PutBucketVersioning — body uses Java enum NAME `ENABLED` ----

func TestPutBucketVersioning_SendsEnabled(t *testing.T) {
	srv, cap := newCapturingServer(t, http.StatusNoContent, "")
	defer srv.Close()

	c := newTestClient(t, srv)
	err := c.PutBucketVersioning(context.Background(), "my-bucket",
		PutBucketVersioningRequest{VersioningConfiguration: &VersioningConfiguration{Status: "ENABLED"}}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	vc, _ := cap.body["versioningConfiguration"].(map[string]any)
	if vc == nil || vc["status"] != "ENABLED" {
		t.Errorf("versioningConfiguration.status = %v, want ENABLED", cap.body["versioningConfiguration"])
	}
	if cap.path != "/panel-main/storage/api/v1/buckets/my-bucket/versioning" {
		t.Errorf("path = %q", cap.path)
	}
}

// ---- 13. PutObjectLockConfiguration — body shape ----

func TestPutObjectLockConfiguration_SendsEnabled(t *testing.T) {
	srv, cap := newCapturingServer(t, http.StatusNoContent, "")
	defer srv.Close()

	c := newTestClient(t, srv)
	err := c.PutObjectLockConfiguration(context.Background(), "my-bucket",
		PutObjectLockConfigurationRequest{ObjectLockConfiguration: &ObjectLockConfiguration{ObjectLockEnabled: "ENABLED"}}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	olc, _ := cap.body["objectLockConfiguration"].(map[string]any)
	if olc == nil || olc["objectLockEnabled"] != "ENABLED" {
		t.Errorf("objectLockConfiguration.objectLockEnabled = %v, want ENABLED", cap.body["objectLockConfiguration"])
	}
	if cap.path != "/panel-main/storage/api/v1/buckets/my-bucket/object-locking" {
		t.Errorf("path = %q", cap.path)
	}
}

// ---- 14. GetObjectLockConfiguration — null response → nil pointer (A6 contract) ----

func TestGetObjectLockConfiguration_NullMeansNotConfigured(t *testing.T) {
	body := `{"success":true,"data":{"objectLockConfiguration":null}}`
	srv, _ := newCapturingServer(t, http.StatusOK, body)
	defer srv.Close()

	c := newTestClient(t, srv)
	got, err := c.GetObjectLockConfiguration(context.Background(), "my-bucket", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for unconfigured object-lock, got %+v", got)
	}
}

// ---- 15. DeleteBucket sends ?forceDestroy=true ----

func TestDeleteBucket_ForceDestroyTrue(t *testing.T) {
	srv, cap := newCapturingServer(t, http.StatusNoContent, "")
	defer srv.Close()

	c := newTestClient(t, srv)
	err := c.DeleteBucket(context.Background(), "my-bucket", true, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cap.method != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", cap.method)
	}
	if !strings.Contains(cap.rawQuery, "forceDestroy=true") {
		t.Errorf("rawQuery = %q, want forceDestroy=true", cap.rawQuery)
	}
}

// ---- 16. DeleteBucket forceDestroy=false; 628 from server → IsNotFound true ----

func TestDeleteBucket_ForceDestroyFalse_AlreadyGone(t *testing.T) {
	body := `{"success":false,"data":null,"errors":[{"code":628,"message":"bucket not found"}]}`
	srv, cap := newCapturingServer(t, http.StatusBadRequest, body)
	defer srv.Close()

	c := newTestClient(t, srv)
	err := c.DeleteBucket(context.Background(), "my-bucket", false, nil)
	if !IsNotFound(err) {
		t.Fatalf("expected IsNotFound=true for 628 on delete, got: %v", err)
	}
	if !strings.Contains(cap.rawQuery, "forceDestroy=false") {
		t.Errorf("rawQuery = %q, want forceDestroy=false", cap.rawQuery)
	}
}

// ---- live test, gated behind PRODATA_LIVE_TEST=1; if hitting prod-kz host,
// also requires PRODATA_ALLOW_PROD_KZ_MUTATION=tf-iac-30455 + tf-iac-30455- name prefix ----

const liveBucketPrefix = "tf-iac-30455-"

func requireProdKzMutationAllowed(t *testing.T, baseURL, bucketName string) {
	t.Helper()
	if !strings.Contains(baseURL, "kz") {
		return
	}
	if os.Getenv("PRODATA_ALLOW_PROD_KZ_MUTATION") != "tf-iac-30455" {
		t.Skip("prod-kz target detected — set PRODATA_ALLOW_PROD_KZ_MUTATION=tf-iac-30455 to allow mutating tests")
	}
	if !strings.HasPrefix(bucketName, liveBucketPrefix) {
		t.Fatalf("prod-kz bucket name %q must start with %q", bucketName, liveBucketPrefix)
	}
}

func TestLive_BucketCRUD(t *testing.T) {
	if os.Getenv("PRODATA_LIVE_TEST") != "1" {
		t.Skip("set PRODATA_LIVE_TEST=1 to run live API tests")
	}

	baseURL := os.Getenv("PRODATA_API_BASE_URL")
	apiKey := os.Getenv("PRODATA_API_KEY_ID")
	apiSecret := os.Getenv("PRODATA_API_SECRET_KEY")
	region := os.Getenv("PRODATA_REGION")
	projectTag := os.Getenv("PRODATA_PROJECT_TAG")
	if baseURL == "" || apiKey == "" || apiSecret == "" || region == "" || projectTag == "" {
		t.Skip("PRODATA_API_BASE_URL/API_KEY_ID/API_SECRET_KEY/REGION/PROJECT_TAG must be set")
	}

	bucketName := liveBucketPrefix + "live-crud-" + strings.ToLower(time.Now().UTC().Format("20060102-150405"))
	requireProdKzMutationAllowed(t, baseURL, bucketName)

	c, err := New(Config{
		APIBaseURL:   baseURL,
		APIKeyID:     apiKey,
		APISecretKey: apiSecret,
		UserAgent:    "tf-provider-prodata/live-test",
		Region:       region,
		ProjectTag:   projectTag,
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	ctx := context.Background()

	if err := c.CreateBucket(ctx, CreateBucketRequest{
		BucketKey: bucketName,
		Acl:       "PRIVATE",
	}, nil); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	t.Cleanup(func() {
		if err := c.DeleteBucket(context.Background(), bucketName, true, nil); err != nil && !IsNotFound(err) {
			t.Logf("cleanup DeleteBucket: %v", err)
		}
	})

	got, err := c.GetBucket(ctx, bucketName, nil)
	if err != nil {
		t.Fatalf("GetBucket: %v", err)
	}
	if got.Name != bucketName {
		t.Errorf("GetBucket name = %q, want %q", got.Name, bucketName)
	}

	if err := c.PutBucketVersioning(ctx, bucketName,
		PutBucketVersioningRequest{VersioningConfiguration: &VersioningConfiguration{Status: "ENABLED"}}, nil); err != nil {
		t.Fatalf("PutBucketVersioning: %v", err)
	}
	v, err := c.GetBucketVersioning(ctx, bucketName, nil)
	if err != nil {
		t.Fatalf("GetBucketVersioning: %v", err)
	}
	if v == nil || v.Status != "ENABLED" {
		t.Errorf("GetBucketVersioning = %+v, want ENABLED", v)
	}

	if err := c.DeleteBucket(ctx, bucketName, false, nil); err != nil {
		t.Fatalf("DeleteBucket(force=false) on empty bucket: %v", err)
	}

	if _, err := c.GetBucket(ctx, bucketName, nil); !IsNotFound(err) {
		t.Errorf("GetBucket after delete: expected IsNotFound, got %v", err)
	}

	if err := c.DeleteBucket(ctx, bucketName, false, nil); !IsNotFound(err) {
		var apiErr *APIError
		if !errors.As(err, &apiErr) || !apiErr.HasCode(628) {
			t.Errorf("idempotent DeleteBucket: expected IsNotFound, got %v", err)
		}
	}
}
