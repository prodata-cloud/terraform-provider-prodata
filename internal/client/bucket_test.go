package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	capture := &captureReq{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.method = r.Method
		capture.path = r.URL.Path
		capture.rawQuery = r.URL.RawQuery
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &capture.body)
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
	return srv, capture
}

// ---- 1. CreateBucket happy path: 201 + empty body returns no error ----

func TestCreateBucket_HappyPath(t *testing.T) {
	srv, capture := newCapturingServer(t, http.StatusCreated, "")
	defer srv.Close()

	c := newTestClient(t, srv)
	err := c.CreateBucket(context.Background(), CreateBucketRequest{
		BucketKey: "my-bucket",
		Acl:       "PRIVATE",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capture.method != http.MethodPost {
		t.Errorf("method = %q, want POST", capture.method)
	}
	if capture.path != "/panel-main/storage/api/v1/buckets" {
		t.Errorf("path = %q", capture.path)
	}
}

// ---- 2. CreateBucket sends Java enum NAMES on the wire (PRIVATE, ENABLED) ----

func TestCreateBucket_SendsEnumNames(t *testing.T) {
	srv, capture := newCapturingServer(t, http.StatusCreated, "")
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

	if got := capture.body["bucketKey"]; got != "my-bucket" {
		t.Errorf("bucketKey = %v, want %q", got, "my-bucket")
	}
	if got := capture.body["acl"]; got != "PUBLIC_READ" {
		t.Errorf("acl = %v, want %q (Java enum NAME, not wire value)", got, "PUBLIC_READ")
	}
	vc, _ := capture.body["versioningConfiguration"].(map[string]any)
	if vc == nil || vc["status"] != "ENABLED" {
		t.Errorf("versioningConfiguration.status = %v, want %q", capture.body["versioningConfiguration"], "ENABLED")
	}
	if got := capture.body["objectLockEnabledForBucket"]; got != true {
		t.Errorf("objectLockEnabledForBucket = %v, want true", got)
	}
	if _, present := capture.body["name"]; present {
		t.Errorf("payload contains forbidden field `name` — must use `bucketKey`: %v", capture.body)
	}
}

// ---- 3. CreateBucket omits versioningConfiguration when nil (object_lock=false case) ----

func TestCreateBucket_OmitsNilVersioning(t *testing.T) {
	srv, capture := newCapturingServer(t, http.StatusCreated, "")
	defer srv.Close()

	c := newTestClient(t, srv)
	err := c.CreateBucket(context.Background(), CreateBucketRequest{
		BucketKey: "my-bucket",
		Acl:       "PRIVATE",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, present := capture.body["versioningConfiguration"]; present {
		t.Errorf("expected versioningConfiguration to be omitted when nil, got: %v", capture.body)
	}
	if _, present := capture.body["objectLockEnabledForBucket"]; present {
		t.Errorf("expected objectLockEnabledForBucket to be omitted when nil, got: %v", capture.body)
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
	srv, capture := newCapturingServer(t, http.StatusOK, body)
	defer srv.Close()

	c := newTestClient(t, srv)
	b, err := c.GetBucket(context.Background(), "my-bucket", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.Name != "my-bucket" || b.CreationDate == "" || b.Size == nil || *b.Size != 1024 {
		t.Errorf("got %+v", b)
	}
	if capture.path != "/panel-main/storage/api/v1/buckets/my-bucket" {
		t.Errorf("path = %q", capture.path)
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
	srv, capture := newCapturingServer(t, http.StatusNoContent, "")
	defer srv.Close()

	c := newTestClient(t, srv)
	err := c.PutBucketAcl(context.Background(), "my-bucket", PutBucketAclRequest{Acl: "PUBLIC_READ_WRITE"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capture.method != http.MethodPut {
		t.Errorf("method = %q, want PUT", capture.method)
	}
	if capture.path != "/panel-main/storage/api/v1/buckets/my-bucket/acl" {
		t.Errorf("path = %q", capture.path)
	}
	if capture.body["acl"] != "PUBLIC_READ_WRITE" {
		t.Errorf("acl = %v, want PUBLIC_READ_WRITE", capture.body["acl"])
	}
	if _, present := capture.body["accessControlPolicy"]; present {
		t.Errorf("expected accessControlPolicy to be omitted when nil, got %v", capture.body)
	}
}

// ---- 12. PutBucketVersioning — body uses Java enum NAME `ENABLED` ----

func TestPutBucketVersioning_SendsEnabled(t *testing.T) {
	srv, capture := newCapturingServer(t, http.StatusNoContent, "")
	defer srv.Close()

	c := newTestClient(t, srv)
	err := c.PutBucketVersioning(context.Background(), "my-bucket",
		PutBucketVersioningRequest{VersioningConfiguration: &VersioningConfiguration{Status: "ENABLED"}}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	vc, _ := capture.body["versioningConfiguration"].(map[string]any)
	if vc == nil || vc["status"] != "ENABLED" {
		t.Errorf("versioningConfiguration.status = %v, want ENABLED", capture.body["versioningConfiguration"])
	}
	if capture.path != "/panel-main/storage/api/v1/buckets/my-bucket/versioning" {
		t.Errorf("path = %q", capture.path)
	}
}

// ---- 13. PutObjectLockConfiguration — body shape ----

func TestPutObjectLockConfiguration_SendsEnabled(t *testing.T) {
	srv, capture := newCapturingServer(t, http.StatusNoContent, "")
	defer srv.Close()

	c := newTestClient(t, srv)
	err := c.PutObjectLockConfiguration(context.Background(), "my-bucket",
		PutObjectLockConfigurationRequest{ObjectLockConfiguration: &ObjectLockConfiguration{ObjectLockEnabled: "ENABLED"}}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	olc, _ := capture.body["objectLockConfiguration"].(map[string]any)
	if olc == nil || olc["objectLockEnabled"] != "ENABLED" {
		t.Errorf("objectLockConfiguration.objectLockEnabled = %v, want ENABLED", capture.body["objectLockConfiguration"])
	}
	if capture.path != "/panel-main/storage/api/v1/buckets/my-bucket/object-locking" {
		t.Errorf("path = %q", capture.path)
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

// ---- 15. DeleteBucket always sends ?forceDestroy=false ----

func TestDeleteBucket_SendsForceDestroyFalse(t *testing.T) {
	srv, capture := newCapturingServer(t, http.StatusNoContent, "")
	defer srv.Close()

	c := newTestClient(t, srv)
	err := c.DeleteBucket(context.Background(), "my-bucket", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capture.method != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", capture.method)
	}
	if !strings.Contains(capture.rawQuery, "forceDestroy=false") {
		t.Errorf("rawQuery = %q, want forceDestroy=false", capture.rawQuery)
	}
}

// ---- 16. DeleteBucket; 628 from server → IsNotFound true ----

func TestDeleteBucket_AlreadyGone(t *testing.T) {
	body := `{"success":false,"data":null,"errors":[{"code":628,"message":"bucket not found"}]}`
	srv, capture := newCapturingServer(t, http.StatusBadRequest, body)
	defer srv.Close()

	c := newTestClient(t, srv)
	err := c.DeleteBucket(context.Background(), "my-bucket", nil)
	if !IsNotFound(err) {
		t.Fatalf("expected IsNotFound=true for 628 on delete, got: %v", err)
	}
	if !strings.Contains(capture.rawQuery, "forceDestroy=false") {
		t.Errorf("rawQuery = %q, want forceDestroy=false", capture.rawQuery)
	}
}

// Full create/read/update/delete behavior against a live API is covered by the
// TF_ACC acceptance suite in the provider package (TestAccS3Bucket_basic), which
// also owns the production-mutation guard and disposable name prefix.
