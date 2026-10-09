---
page_title: "prodata_kubernetes_kubeconfig Ephemeral Resource - ProData Provider"
subcategory: "Kubernetes"
description: |-
  Reads the connection details of a ProData Managed Kubernetes cluster without writing them to Terraform state or to a saved plan.
---

# prodata_kubernetes_kubeconfig (Ephemeral Resource)

Reads the connection details of a ProData Managed Kubernetes cluster — API server address, CA certificate, client certificate and key, token, and the full kubeconfig — **without writing them to Terraform state or to a saved plan**. Use it to configure the `kubernetes` and `helm` providers.

The `kube_config` attribute of the [`prodata_kubernetes_cluster`](../resources/kubernetes_cluster.md) resource and data source holds the same values, and they stay in the state file, readable by anyone who can read the state. They are cluster-admin credentials, and Kubernetes cannot revoke a client certificate. An ephemeral resource exists only while Terraform runs: it is read when Terraform plans or applies, handed to the configuration that uses it, and discarded.

~> **Note:** Ephemeral resources need **Terraform 1.10 or later**. An older Terraform does not recognize the `ephemeral` block.

To keep the credentials out of the state altogether, also set `exclude_credentials_from_state = true` on the cluster resource and on every data source that reads the cluster — otherwise they are still stored there. See [Credentials in Terraform state](../resources/kubernetes_cluster.md#credentials-in-terraform-state).

## Example Usage

```terraform
# Look up an existing cluster. exclude_credentials_from_state keeps its credentials out of
# the state this data source writes.
data "prodata_kubernetes_cluster" "main" {
  name                           = "prod-cluster"
  exclude_credentials_from_state = true
}

# The cluster's connection details, held in memory for this Terraform run only: never
# written to the state or to a saved plan. Requires Terraform 1.10 or later.
ephemeral "prodata_kubernetes_kubeconfig" "main" {
  cluster_id = data.prodata_kubernetes_cluster.main.id
}

# The certificate fields are base64 as they appear in the kubeconfig, so wrap them in
# base64decode(). Configure the provider from these values alone: do not also set
# config_path (or KUBE_CONFIG_PATH), which the provider would read and mix in.
provider "kubernetes" {
  host                   = ephemeral.prodata_kubernetes_kubeconfig.main.host
  cluster_ca_certificate = base64decode(ephemeral.prodata_kubernetes_kubeconfig.main.cluster_ca_certificate)
  client_certificate     = base64decode(ephemeral.prodata_kubernetes_kubeconfig.main.client_certificate)
  client_key             = base64decode(ephemeral.prodata_kubernetes_kubeconfig.main.client_key)
}
```

An ephemeral value can be used only where Terraform allows it: in a provider configuration, a provisioner or connection block, a local value, the configuration of another ephemeral resource, an ephemeral variable, an `ephemeral = true` output of a child module, or — from Terraform 1.11 — a write-only argument. It cannot be assigned to an ordinary resource argument or to a regular output: Terraform refuses, so that it cannot reach the state by accident.

## Behavior

- **It is read again on every plan and apply.** Each read is one lookup of the cluster, and nothing is kept between runs.
- **It fails rather than return empty values.** The read is an error when the cluster does not exist in this region and project, when it has been deleted, when the panel has not produced a kubeconfig for it yet, when the kubeconfig names no API server address, or when it holds neither a client certificate with its key nor a token. This is deliberate: a `kubernetes` or `helm` provider that is given no host or no credentials does not stop — it may fall back to other connection settings it finds, such as a kubeconfig named by `config_path` or the `KUBE_CONFIG_PATH` environment variable, or the service account of the pod it runs in, and act on whichever cluster those name.
- **Do not also give the provider a kubeconfig file.** Failing instead of returning empty values protects against a provider that receives nothing; it cannot protect against one that also receives a file. When `config_path` or `config_paths` is set in the `kubernetes` or `helm` provider block, or `KUBE_CONFIG_PATH` or `KUBE_CONFIG_PATHS` in the environment, the provider loads that file and applies the values you configure on top of it. Anything you leave unset — a token, an `exec` plugin, a username and password — is taken from the file's current context, so a credential that belongs to another cluster can be sent to this cluster's API server together with the client certificate. When you configure a provider from this resource, leave those arguments out of its block and unset those variables.
- **The cluster's status is checked only for `DELETED`.** A cluster for which the panel has a kubeconfig yields it in any other status, including while it upgrades. The panel fetches the kubeconfig lazily, so it can lag briefly behind a new cluster reaching `SUCCESS`; until it appears, the read fails with an error that says so.
- **If `cluster_id` is not known until apply, none of this protects the provider.** That is the case while the cluster is created or replaced in the same run: Terraform defers opening the resource to apply, and the provider you configure from it receives unknown values while Terraform plans. A provider may treat unknown configuration like missing configuration and fall back to the connection settings described above — and plan any Kubernetes objects already in the state against whatever cluster those name. Keep the cluster and the workloads on it in separate configurations (the example above reads an existing cluster with a data source). Putting both in one configuration works for the run that creates the cluster, but any later run that replaces the cluster plans the Kubernetes objects in it against a provider with unknown configuration.

## Schema

### Required

- `cluster_id` (Number) ID of the Kubernetes cluster, for example `prodata_kubernetes_cluster.main.id`. Minimum `1`.

### Optional

- `region` (String) Region ID override. If omitted, uses the provider's default region.
- `project_tag` (String) Project tag override. If omitted, uses the provider default.

### Attribute Reference

All of these are sensitive, like the `kube_config` object they mirror: Terraform redacts them wherever it would print them.

- `host` (String, Sensitive) Kubernetes API server URL.
- `cluster_ca_certificate` (String, Sensitive) Base64-encoded cluster CA certificate, exactly as it appears in the kubeconfig. Wrap it in `base64decode()` when passing it to the `kubernetes` provider.
- `client_certificate` (String, Sensitive) Base64-encoded client certificate for cluster-admin access. Null when the kubeconfig has none (token auth). Wrap it in `base64decode()` when passing it to the `kubernetes` provider.
- `client_key` (String, Sensitive) Base64-encoded client key for cluster-admin access. Null when the kubeconfig has none (token auth). Wrap it in `base64decode()` when passing it to the `kubernetes` provider.
- `token` (String, Sensitive) Bearer token, when the cluster uses token auth. Null otherwise.
- `raw_config` (String, Sensitive) The full kubeconfig as plain YAML.
