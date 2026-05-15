package datasources

import (
	"testing"

	"terraform-provider-prodata/internal/client"
)

// Unit tests for the small TF<->panel mapping helpers used by the
// prodata_s3_bucket data source. Full HTTP round-trip behavior for GetBucket /
// ListBuckets / GetBucketVersioning / GetObjectLockConfiguration is already
// covered by internal/client/bucket_test.go (16 cases including happy path,
// 712 cross-project, 628 not-found, and ListBuckets pagination).

func TestVersioningFromConfig(t *testing.T) {
	cases := []struct {
		label string
		in    *client.VersioningConfiguration
		want  string
	}{
		{"nil (never configured)", nil, "disabled"},
		{"ENABLED", &client.VersioningConfiguration{Status: "ENABLED"}, "enabled"},
		{"SUSPENDED", &client.VersioningConfiguration{Status: "SUSPENDED"}, "suspended"},
		{"unknown status falls back to disabled", &client.VersioningConfiguration{Status: "ZARGON"}, "disabled"},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			if got := versioningFromConfig(c.in); got != c.want {
				t.Errorf("versioningFromConfig(%+v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestObjectLockFromConfig(t *testing.T) {
	cases := []struct {
		label string
		in    *client.ObjectLockConfiguration
		want  bool
	}{
		{"nil (panel A6 maps NotFound -> null)", nil, false},
		{"ENABLED -> true", &client.ObjectLockConfiguration{ObjectLockEnabled: "ENABLED"}, true},
		{"unknown enum -> false", &client.ObjectLockConfiguration{ObjectLockEnabled: "SOMETHING"}, false},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			if got := objectLockFromConfig(c.in); got != c.want {
				t.Errorf("objectLockFromConfig(%+v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
