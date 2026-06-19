package client

import (
	"encoding/base64"
	"testing"
)

// A current-context that names a user not present in the file must NOT fall back
// to the first user (which would export the wrong credentials). That field stays
// empty, while host/CA from the named, existing cluster are still returned.
func TestParseKubeConfig_StrictContextMissingUser(t *testing.T) {
	const cfg = `apiVersion: v1
current-context: ctx
clusters:
- name: real
  cluster:
    server: https://real:6443
    certificate-authority-data: Q0E=
users:
- name: other
  user:
    client-certificate-data: V1JPTkc=
contexts:
- name: ctx
  context:
    cluster: real
    user: missing
`
	kc := ParseKubeConfig(base64.StdEncoding.EncodeToString([]byte(cfg)))
	if kc == nil {
		t.Fatal("nil")
	}
	if kc.Host != "https://real:6443" {
		t.Errorf("Host = %q, want the named cluster", kc.Host)
	}
	if kc.ClientCertificate != "" {
		t.Errorf("ClientCertificate = %q, want empty (named user missing — no wrong-user fallback)", kc.ClientCertificate)
	}
}

// A current-context that does not exist in contexts must not fall back to the
// first cluster/user (the kubeconfig is malformed — return raw-only).
func TestParseKubeConfig_StrictContextMissingContext(t *testing.T) {
	const cfg = `apiVersion: v1
current-context: nope
clusters:
- name: real
  cluster:
    server: https://real:6443
users:
- name: u
  user:
    token: tok
`
	kc := ParseKubeConfig(base64.StdEncoding.EncodeToString([]byte(cfg)))
	if kc == nil {
		t.Fatal("nil")
	}
	if kc.Host != "" || kc.Token != "" {
		t.Errorf("got host=%q token=%q, want both empty for an unresolvable current-context", kc.Host, kc.Token)
	}
}

// Raw (unpadded) standard base64 must still decode rather than degrade to raw-only.
func TestParseKubeConfig_RawStdBase64(t *testing.T) {
	const cfg = `apiVersion: v1
clusters:
- name: c
  cluster:
    server: https://h:6443
`
	secret := base64.RawStdEncoding.EncodeToString([]byte(cfg)) // no '=' padding
	kc := ParseKubeConfig(secret)
	if kc == nil {
		t.Fatal("nil")
	}
	if kc.Host != "https://h:6443" {
		t.Errorf("Host = %q, want decoded host from raw-std base64", kc.Host)
	}
}
