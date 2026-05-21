package resources

import (
	"strings"
	"testing"
)

// Unit tests for the prodata_s3_bucket resource's plan-time validation logic.
// These exercise the pure-function helpers we wrote (name rules, the object-lock
// cross-field rule, and import-id parsing) — not the framework validators wired
// into the schema. Full create/read/update/delete behavior is covered by the
// TF_ACC acceptance suite in the provider package.

// Invalid name characters are rejected by our validator.
func TestS3Bucket_NameRegexRejectsBadChars(t *testing.T) {
	for _, name := range []string{"MyBucket", "my_bucket", "my bucket", "my/bucket", "myBucket"} {
		if msg := validateBucketNameStr(name); msg == "" {
			t.Errorf("expected %q to fail validation, but it passed", name)
		}
	}
}

// Invalid name length (both too short and too long) is rejected by our validator.
func TestS3Bucket_NameLengthRejects(t *testing.T) {
	cases := map[string]string{
		"too-short": "ab",
		"too-long":  strings.Repeat("a", 25),
	}
	for label, name := range cases {
		t.Run(label, func(t *testing.T) {
			msg := validateBucketNameStr(name)
			if msg == "" {
				t.Errorf("expected %q to fail length validation, but it passed", name)
			}
			if !strings.Contains(msg, "3-24") {
				t.Errorf("expected length error to mention 3-24, got: %s", msg)
			}
		})
	}
}

// object_lock_enabled=true requires versioning=true (our cross-field rule).
func TestS3Bucket_ObjectLockRequiresEnabledVersioning(t *testing.T) {
	cases := []struct {
		objectLock bool
		versioning bool
		wantErr    bool
	}{
		{true, false, true},
		{true, true, false},
		{false, false, false},
		{false, true, false},
	}
	for _, c := range cases {
		msg := validateObjectLockRequiresVersioning(c.objectLock, c.versioning)
		if c.wantErr && msg == "" {
			t.Errorf("expected error for object_lock=%v, versioning=%v; got none", c.objectLock, c.versioning)
		}
		if !c.wantErr && msg != "" {
			t.Errorf("unexpected error for object_lock=%v, versioning=%v: %s", c.objectLock, c.versioning, msg)
		}
	}
}

// Happy path: valid names and a valid object_lock+versioning combo pass.
func TestS3Bucket_HappyPathValidations(t *testing.T) {
	for _, name := range []string{"my-bucket", "logs.2026", "abc", "a-b-c"} {
		if msg := validateBucketNameStr(name); msg != "" {
			t.Errorf("happy-path name %q rejected: %s", name, msg)
		}
	}
	if msg := validateObjectLockRequiresVersioning(true, true); msg != "" {
		t.Errorf("happy-path object_lock+versioning rejected: %s", msg)
	}
}

// Import ID parsing: {region}/{name}@{project_tag}, with deterministic rejection
// of malformed inputs.
func TestS3Bucket_ParseImportID(t *testing.T) {
	good := []struct {
		in                       string
		region, name, projectTag string
	}{
		{"UZ-5/my-bucket@my-project", "UZ-5", "my-bucket", "my-project"},
		{"KZ-1/my.bucket.v2@prod", "KZ-1", "my.bucket.v2", "prod"},
		{"UZ-3/a-b-c@p1", "UZ-3", "a-b-c", "p1"},
	}
	for _, c := range good {
		r, n, p, ok := parseImportID(c.in)
		if !ok {
			t.Errorf("expected %q to parse, got ok=false", c.in)
			continue
		}
		if r != c.region || n != c.name || p != c.projectTag {
			t.Errorf("parse %q: got (%q,%q,%q), want (%q,%q,%q)", c.in, r, n, p, c.region, c.name, c.projectTag)
		}
	}
	for _, bad := range []string{"", "UZ-5/my-bucket", "UZ-5/@p", "/my-bucket@p", "UZ-5/my-bucket@", "no-slash@p", "UZ-5"} {
		if _, _, _, ok := parseImportID(bad); ok {
			t.Errorf("expected %q to fail parsing, but ok=true", bad)
		}
	}
}
