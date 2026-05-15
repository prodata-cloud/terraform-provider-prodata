package resources

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Unit tests for the prodata_s3_bucket resource's plan-time validation logic.
// These exercise the extracted pure-function helpers + the framework validator
// shims; full Create/Read/Update/Delete acceptance coverage is in the separate
// TF_ACC=1 suite (step 2.5 / 2.6 of the task-30455 plan).

// Test 1 — invalid name regex.
func TestS3Bucket_NameRegexRejectsBadChars(t *testing.T) {
	for _, name := range []string{"MyBucket", "my_bucket", "my bucket", "my/bucket", "myBucket"} {
		if msg := validateBucketNameStr(name); msg == "" {
			t.Errorf("expected %q to fail validation, but it passed", name)
		}
	}
}

// Test 2 — invalid name length (both too short and too long).
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

// Test 3 — invalid acl enum.
func TestS3Bucket_AclOneOfRejectsBadValue(t *testing.T) {
	v := stringvalidator.OneOf("private", "public-read", "public-read-write")
	for _, bad := range []string{"public", "PRIVATE", "public-read-only", "world-readable", ""} {
		req := validator.StringRequest{
			Path:        path.Root("acl"),
			ConfigValue: types.StringValue(bad),
		}
		resp := &validator.StringResponse{}
		v.ValidateString(context.Background(), req, resp)
		if !resp.Diagnostics.HasError() {
			t.Errorf("expected acl=%q to fail OneOf validation, but it passed", bad)
		}
	}
}

// Test 4 — invalid versioning enum.
func TestS3Bucket_VersioningOneOfRejectsBadValue(t *testing.T) {
	v := stringvalidator.OneOf("enabled", "suspended", "disabled")
	for _, bad := range []string{"ENABLED", "true", "on", "off", "paused", ""} {
		req := validator.StringRequest{
			Path:        path.Root("versioning"),
			ConfigValue: types.StringValue(bad),
		}
		resp := &validator.StringResponse{}
		v.ValidateString(context.Background(), req, resp)
		if !resp.Diagnostics.HasError() {
			t.Errorf("expected versioning=%q to fail OneOf validation, but it passed", bad)
		}
	}
}

// Test 5 — object_lock_enabled=true requires versioning=enabled.
func TestS3Bucket_ObjectLockRequiresEnabledVersioning(t *testing.T) {
	cases := []struct {
		objectLock bool
		versioning string
		wantErr    bool
	}{
		{true, "disabled", true},
		{true, "suspended", true},
		{true, "enabled", false},
		{false, "disabled", false},
		{false, "enabled", false},
	}
	for _, c := range cases {
		msg := validateObjectLockRequiresVersioning(c.objectLock, c.versioning)
		if c.wantErr && msg == "" {
			t.Errorf("expected error for object_lock=%v, versioning=%q; got none", c.objectLock, c.versioning)
		}
		if !c.wantErr && msg != "" {
			t.Errorf("unexpected error for object_lock=%v, versioning=%q: %s", c.objectLock, c.versioning, msg)
		}
	}
}

// Test 6 — versioning transition enabled→disabled is rejected.
func TestS3Bucket_VersioningEnabledToDisabledRejected(t *testing.T) {
	if msg := validateVersioningTransition("enabled", "disabled"); msg == "" {
		t.Fatal("expected enabled→disabled to be rejected, got no error")
	}
}

// Test 7 — versioning transition suspended→disabled is rejected.
func TestS3Bucket_VersioningSuspendedToDisabledRejected(t *testing.T) {
	if msg := validateVersioningTransition("suspended", "disabled"); msg == "" {
		t.Fatal("expected suspended→disabled to be rejected, got no error")
	}
}

// Test 8 — happy path: valid name, valid object_lock+versioning combo, legal
// transitions all pass without error.
func TestS3Bucket_HappyPathValidations(t *testing.T) {
	for _, name := range []string{"my-bucket", "logs.2026", "tf-iac-30455-x", "abc"} {
		if msg := validateBucketNameStr(name); msg != "" {
			t.Errorf("happy-path name %q rejected: %s", name, msg)
		}
	}
	if msg := validateObjectLockRequiresVersioning(true, "enabled"); msg != "" {
		t.Errorf("happy-path object_lock+versioning rejected: %s", msg)
	}
	legalTransitions := [][2]string{
		{"disabled", "enabled"},
		{"disabled", "disabled"},
		{"enabled", "suspended"},
		{"suspended", "enabled"},
		{"enabled", "enabled"},
	}
	for _, p := range legalTransitions {
		if msg := validateVersioningTransition(p[0], p[1]); msg != "" {
			t.Errorf("happy-path transition %q→%q rejected: %s", p[0], p[1], msg)
		}
	}
}

// Bonus — import ID parser covers a path the acceptance suite implicitly exercises
// but with cheap unit coverage so a malformed user input gets a deterministic error.
func TestS3Bucket_ParseImportID(t *testing.T) {
	good := []struct {
		in                            string
		region, name, projectTag      string
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
