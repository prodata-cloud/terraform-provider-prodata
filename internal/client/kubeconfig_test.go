package client

import (
	"encoding/base64"
	"testing"
)

const sampleKubeconfig = `apiVersion: v1
kind: Config
current-context: prodata
clusters:
- name: prodata
  cluster:
    server: https://10.0.0.10:6443
    certificate-authority-data: Q0FfREFUQQ==
users:
- name: admin
  user:
    client-certificate-data: Q0xJRU5UX0NFUlQ=
    client-key-data: Q0xJRU5UX0tFWQ==
contexts:
- name: prodata
  context:
    cluster: prodata
    user: admin
`

func TestParseKubeConfig_Structured(t *testing.T) {
	secret := base64.StdEncoding.EncodeToString([]byte(sampleKubeconfig))

	kc := ParseKubeConfig(secret)
	if kc == nil {
		t.Fatal("ParseKubeConfig returned nil for a valid secret")
	}
	if kc.Host != "https://10.0.0.10:6443" {
		t.Errorf("Host = %q, want https://10.0.0.10:6443", kc.Host)
	}
	if kc.ClusterCACertificate != "Q0FfREFUQQ==" {
		t.Errorf("ClusterCACertificate = %q, want the base64 ca data verbatim", kc.ClusterCACertificate)
	}
	if kc.ClientCertificate != "Q0xJRU5UX0NFUlQ=" {
		t.Errorf("ClientCertificate = %q", kc.ClientCertificate)
	}
	if kc.ClientKey != "Q0xJRU5UX0tFWQ==" {
		t.Errorf("ClientKey = %q", kc.ClientKey)
	}
	if kc.Raw != sampleKubeconfig {
		t.Errorf("Raw was not the decoded plain YAML; got %q", kc.Raw)
	}
}

func TestParseKubeConfig_Empty(t *testing.T) {
	if kc := ParseKubeConfig(""); kc != nil {
		t.Errorf("ParseKubeConfig(\"\") = %+v, want nil", kc)
	}
	if kc := ParseKubeConfig("   "); kc != nil {
		t.Errorf("ParseKubeConfig(whitespace) = %+v, want nil", kc)
	}
}

func TestParseKubeConfig_CurrentContextSelectsNonFirst(t *testing.T) {
	const twoContext = `apiVersion: v1
kind: Config
current-context: second
clusters:
- name: first
  cluster:
    server: https://first:6443
    certificate-authority-data: Rg==
- name: second
  cluster:
    server: https://second:6443
    certificate-authority-data: Uw==
users:
- name: first-user
  user:
    token: first-token
- name: second-user
  user:
    token: second-token
contexts:
- name: first
  context:
    cluster: first
    user: first-user
- name: second
  context:
    cluster: second
    user: second-user
`
	secret := base64.StdEncoding.EncodeToString([]byte(twoContext))

	kc := ParseKubeConfig(secret)
	if kc == nil {
		t.Fatal("ParseKubeConfig returned nil")
	}
	if kc.Host != "https://second:6443" {
		t.Errorf("Host = %q, want the current-context (second) cluster", kc.Host)
	}
	if kc.Token != "second-token" {
		t.Errorf("Token = %q, want the current-context (second) user token", kc.Token)
	}
}

func TestParseKubeConfig_RawOnlyOnGarbage(t *testing.T) {
	// Valid base64 that decodes to text which is not a kubeconfig: structured
	// fields stay empty, Raw is still populated so the blob is not lost.
	secret := base64.StdEncoding.EncodeToString([]byte("not a kubeconfig: [unterminated"))
	kc := ParseKubeConfig(secret)
	if kc == nil {
		t.Fatal("ParseKubeConfig returned nil for a non-empty secret")
	}
	if kc.Host != "" || kc.ClientCertificate != "" {
		t.Errorf("expected empty structured fields, got host=%q cert=%q", kc.Host, kc.ClientCertificate)
	}
	if kc.Raw == "" {
		t.Error("Raw should be populated even when structured parsing fails")
	}
}
