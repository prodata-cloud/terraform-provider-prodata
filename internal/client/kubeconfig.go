package client

import (
	"encoding/base64"
	"strings"

	"gopkg.in/yaml.v3"
)

// KubeConfig is the structured, Terraform-ready view of a cluster's kubeconfig,
// parsed from the base64 clusterConfigSecret the panel returns. It lets a user
// wire the `kubernetes` / `helm` providers directly instead of decoding an opaque
// blob.
//
// The certificate fields are kept in their kubeconfig base64 form (the
// `*-data` values, as they appear in the file). This matches the Azure/DO
// convention: callers pass them through `base64decode()` when configuring the
// kubernetes provider. Raw is the full kubeconfig as plain YAML.
type KubeConfig struct {
	Host                 string
	ClusterCACertificate string
	ClientCertificate    string
	ClientKey            string
	Token                string
	Raw                  string
}

// kubeconfigFile is the minimal subset of the kubeconfig schema the provider
// reads. Field names are the kubeconfig YAML keys.
type kubeconfigFile struct {
	CurrentContext string `yaml:"current-context"`
	Clusters       []struct {
		Name    string `yaml:"name"`
		Cluster struct {
			Server                   string `yaml:"server"`
			CertificateAuthorityData string `yaml:"certificate-authority-data"`
		} `yaml:"cluster"`
	} `yaml:"clusters"`
	Users []struct {
		Name string `yaml:"name"`
		User struct {
			ClientCertificateData string `yaml:"client-certificate-data"`
			ClientKeyData         string `yaml:"client-key-data"`
			Token                 string `yaml:"token"`
		} `yaml:"user"`
	} `yaml:"users"`
	Contexts []struct {
		Name    string `yaml:"name"`
		Context struct {
			Cluster string `yaml:"cluster"`
			User    string `yaml:"user"`
		} `yaml:"context"`
	} `yaml:"contexts"`
}

// ParseKubeConfig decodes the base64 clusterConfigSecret and extracts the
// connection fields for the structured kube_config attribute. It returns nil for
// an empty secret (the cluster has no kubeconfig yet — NEW/PROCESSING).
//
// It is intentionally lenient: a secret that is present but not valid base64, or
// not parseable YAML, still yields a KubeConfig with Raw populated (best effort)
// so the caller can surface the raw blob rather than erroring a Read. The
// connection fields are taken from the current-context's cluster/user when a
// current-context is set, else from the first cluster/user.
func ParseKubeConfig(secret string) *KubeConfig {
	if strings.TrimSpace(secret) == "" {
		return nil
	}

	kc := &KubeConfig{}
	raw := decodeMaybeBase64(strings.TrimSpace(secret))
	if raw == nil {
		// Not base64 in any common encoding — treat the secret as already-plain YAML.
		raw = []byte(secret)
	}
	kc.Raw = string(raw)

	var f kubeconfigFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return kc // raw-only; structured fields stay empty
	}

	// Resolve the current-context's cluster/user. When a current-context is set it
	// is authoritative: the named cluster and user must exist exactly — we never
	// fall back to the first entry, which could export a wrong host/credential
	// pairing. The first cluster/user is used only when there is no current-context
	// (the common single-entry admin kubeconfig).
	hasContext := f.CurrentContext != ""
	clusterName, userName := "", ""
	if hasContext {
		for _, c := range f.Contexts {
			if c.Name == f.CurrentContext {
				clusterName, userName = c.Context.Cluster, c.Context.User
				break
			}
		}
	}

	switch {
	case clusterName != "":
		for i := range f.Clusters {
			if f.Clusters[i].Name == clusterName {
				kc.Host = f.Clusters[i].Cluster.Server
				kc.ClusterCACertificate = f.Clusters[i].Cluster.CertificateAuthorityData
				break
			}
		}
	case !hasContext && len(f.Clusters) > 0:
		kc.Host = f.Clusters[0].Cluster.Server
		kc.ClusterCACertificate = f.Clusters[0].Cluster.CertificateAuthorityData
	}

	switch {
	case userName != "":
		for i := range f.Users {
			if f.Users[i].Name == userName {
				kc.ClientCertificate = f.Users[i].User.ClientCertificateData
				kc.ClientKey = f.Users[i].User.ClientKeyData
				kc.Token = f.Users[i].User.Token
				break
			}
		}
	case !hasContext && len(f.Users) > 0:
		kc.ClientCertificate = f.Users[0].User.ClientCertificateData
		kc.ClientKey = f.Users[0].User.ClientKeyData
		kc.Token = f.Users[0].User.Token
	}

	return kc
}

// decodeMaybeBase64 tries the common base64 encodings (the panel returns standard
// padded base64 today, but a backend change to raw or URL-safe base64 must not
// silently degrade to raw-only). Returns nil when the input is not base64 in any
// of them — the caller then treats it as already-plain YAML. A plain kubeconfig
// never decodes here: YAML's newlines/colons/spaces are not valid base64.
func decodeMaybeBase64(s string) []byte {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	} {
		if decoded, err := enc.DecodeString(s); err == nil {
			return decoded
		}
	}
	return nil
}
