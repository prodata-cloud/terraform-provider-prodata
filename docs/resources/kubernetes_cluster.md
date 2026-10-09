---
page_title: "prodata_kubernetes_cluster Resource - ProData Provider"
subcategory: "Kubernetes"
description: |-
  Manages a ProData Managed Kubernetes cluster's control plane; worker pools are managed as independent prodata_kubernetes_node_pool resources.
---

# prodata_kubernetes_cluster (Resource)

Manages a ProData Managed Kubernetes cluster's **control plane**. Worker capacity is managed as independent [`prodata_kubernetes_node_pool`](kubernetes_node_pool.md) resources — including the first pool. A cluster with zero node pools is a valid, control-plane-only steady state.

Cluster creation is asynchronous; `terraform apply` blocks until the cluster reaches `SUCCESS` (with a bounded grace period for the lazily-fetched kubeconfig) or the create timeout elapses. If the kubeconfig still lags after the grace, the apply finishes with a warning and `terraform refresh` populates it. (With `exclude_credentials_from_state` set there is nothing to populate: the warning says so, and the ephemeral resource fails until the kubeconfig is available.)

~> **Note:** The cluster's credentials — `kube_config` and, when the platform generated the nodes' SSH key pair, `private_key_encoded` — are stored in Terraform state by default. Marking them `Sensitive` only hides them from the console; the state file holds them in plaintext. To keep them out, set [`exclude_credentials_from_state`](#credentials-in-terraform-state) and read the kubeconfig with the [`prodata_kubernetes_kubeconfig`](../ephemeral-resources/kubernetes_kubeconfig.md) ephemeral resource.

~> **Note:** The cluster is created in the region the API resolves for the request. If your account spans multiple regions, set the region through the provider configuration (or `PRODATA_REGION`) to be sure the cluster lands where you intend. `region` and `project_tag` are fixed at create time — changing either forces a new resource.

~> **Note:** The networking inputs are immutable — changing `network_id`, `pod_cidr`, or `node_ip_range` forces a new resource. `network_id` is additionally **write-once**: the API does not return it, so it is preserved in state and accepted from configuration after `terraform import` without forcing a replacement. `pod_cidr` and `node_ip_range` are read back normally — `node_ip_range` is `Optional`/`Computed`, so when you omit it the platform auto-allocates a free range from `network_id` and records it in state.

~> **Note:** An explicit `node_ip_range` is validated before the cluster is created. A malformed or reversed value is rejected on every plan. Against the local network in `network_id` it must lie inside the network's CIDR and must not contain the network's gateway, otherwise the plan fails; a range that includes the network or broadcast address only produces a warning.

- The network check needs the network to be readable in the cluster's region and project. If it cannot be read, the check is skipped with a warning.
- If `network_id` is not known at plan time (for example the network is created in the same apply), the check runs during apply, after the network exists and before the cluster is created.
- An in-place update or a plain `terraform destroy` of an existing cluster does not run the network check while `node_ip_range` and `network_id` are unchanged. A **replacement** — for any reason, such as a changed `name`, `pod_cidr`, flavor or region, or `-replace` — creates a new cluster and is checked like a create; during such a replacement the plan shows only the errors, and warnings appear during apply. If a replacement-forcing change is pending in the configuration, `terraform destroy` runs the same check; use `terraform destroy -refresh=false` or revert that change.
- Clusters created in the web console often hold a range that starts at the network address or contains the gateway. They keep working, but to replace such a cluster you must give it a valid `node_ip_range`.
- Omitting `node_ip_range` produces a warning on create, because the platform sizes the automatic range from the capacity known at creation time and never widens it.
- To keep an existing cluster whose stored `node_ip_range` no longer passes validation, remove the attribute from the configuration: the stored value stays in state and nothing is replaced. Editing the value forces a new cluster.

## Example Usage

### Fixed-size, highly-available cluster

```terraform
data "prodata_kubernetes_versions" "stable" {}

data "prodata_kubernetes_flavors" "standard" {
  high_availability = false
}

resource "prodata_kubernetes_cluster" "main" {
  name               = "prod-cluster"
  kubernetes_version = data.prodata_kubernetes_versions.stable.latest_version
  network_id         = prodata_local_network.k8s.id
  pod_cidr           = "10.244.0.0/16"
  # node_ip_range omitted — auto-allocated from network_id and reported back in state.
  high_availability  = true
  control_plane_size = "medium" # picks the HA master flavor for you
}

resource "prodata_kubernetes_node_pool" "main_workers" {
  cluster_id = prodata_kubernetes_cluster.main.id
  name       = "workers"
  vcpu       = 4
  ram        = 8
  disk_size  = 80
  node_count = 3
}
```

### Cluster with a public endpoint and an autoscaling worker pool

```terraform
resource "prodata_kubernetes_cluster" "edge" {
  name               = "edge-cluster"
  kubernetes_version = "v1.31.4"
  network_id         = prodata_local_network.k8s.id
  pod_cidr           = "10.245.0.0/16"
  node_ip_range      = "10.0.0.30-10.0.0.40" # explicit range (optional)
  master_flavor_id   = data.prodata_kubernetes_flavors.standard.flavors[0].id

  public_endpoint_enabled = true
  ssh_access_enabled      = true
  public_key              = file(pathexpand("~/.ssh/id_ed25519.pub"))
}

resource "prodata_kubernetes_node_pool" "edge_workers" {
  cluster_id = prodata_kubernetes_cluster.edge.id
  name       = "workers"
  vcpu       = 2
  ram        = 4
  disk_size  = 40

  autoscaling = {
    min_nodes = 1
    max_nodes = 5
  }
}
```

### Wiring the kubernetes provider from `kube_config`

```terraform
provider "kubernetes" {
  host                   = prodata_kubernetes_cluster.main.kube_config.host
  cluster_ca_certificate = base64decode(prodata_kubernetes_cluster.main.kube_config.cluster_ca_certificate)
  client_certificate     = base64decode(prodata_kubernetes_cluster.main.kube_config.client_certificate)
  client_key             = base64decode(prodata_kubernetes_cluster.main.kube_config.client_key)
}
```

### Keeping the credentials out of Terraform state

```terraform
resource "prodata_kubernetes_cluster" "secure" {
  name               = "secure-cluster"
  kubernetes_version = "v1.31.4"
  network_id         = prodata_local_network.k8s.id
  pod_cidr           = "10.247.0.0/16"
  control_plane_size = "small"

  # Authorize your own key when you create the cluster: the platform then generates no key
  # pair, so there is no private key.
  ssh_access_enabled = true
  public_key         = file(pathexpand("~/.ssh/id_ed25519.pub"))

  # kube_config and private_key_encoded are always null: they never reach the state.
  exclude_credentials_from_state = true
}

# Read the kubeconfig when Terraform runs instead (Terraform 1.10 or later). The values are
# held in memory only, never written to the state or to a saved plan.
ephemeral "prodata_kubernetes_kubeconfig" "secure" {
  cluster_id = prodata_kubernetes_cluster.secure.id
}

# Configure the provider from these values alone: do not also set config_path (or
# KUBE_CONFIG_PATH), which the provider would read and mix in.
provider "kubernetes" {
  host                   = ephemeral.prodata_kubernetes_kubeconfig.secure.host
  cluster_ca_certificate = base64decode(ephemeral.prodata_kubernetes_kubeconfig.secure.cluster_ca_certificate)
  client_certificate     = base64decode(ephemeral.prodata_kubernetes_kubeconfig.secure.client_certificate)
  client_key             = base64decode(ephemeral.prodata_kubernetes_kubeconfig.secure.client_key)
}
```

Here the cluster is created in the same configuration, so `cluster_id` is not known until apply: Terraform opens the ephemeral resource then, and the `kubernetes` provider is configured with unknown values while it plans. The protections described under [Behavior](../ephemeral-resources/kubernetes_kubeconfig.md#behavior) do not apply to that plan. That is acceptable for the run that creates the cluster, but any later run that replaces the cluster plans the Kubernetes objects in the same configuration against a provider with unknown configuration. Manage the cluster and the workloads on it in separate configurations, and read the cluster in the workloads' configuration with a data source, as in the [ephemeral resource's example](../ephemeral-resources/kubernetes_kubeconfig.md#example-usage).

## Schema

### Required

- `name` (String) Cluster name. 3-24 characters, lowercase letters / digits / hyphens, not starting or ending with a hyphen. Must be unique across your whole account. Changing it forces a new resource.
- `kubernetes_version` (String) Kubernetes version (e.g. `v1.31.4`). Must be a version offered by the [`prodata_kubernetes_versions`](../data-sources/kubernetes_versions.md) data source. Upgrading is applied in place (asynchronous rollout).
- `network_id` (Number) Local network ID the cluster's nodes attach to. Minimum `1`. Write-once (not read back from the API); changing it forces a new resource.
- `pod_cidr` (String) Pod network CIDR. Must be a `/16` (e.g. `10.244.0.0/16`). Changing it forces a new resource.
### Optional

> Set **exactly one** of `control_plane_size` or `master_flavor_id` to size the control plane.

- `control_plane_size` (String) Control-plane size class — `small`, `medium`, or `large`. A convenience alias that selects the master flavor for you based on `high_availability`: the provider maps the size onto the region's master-flavor catalog by capacity (smallest → `small`). Mutually exclusive with `master_flavor_id`. Changing it forces a new resource.
- `master_flavor_id` (Number) Master node configuration (flavor) ID, from the [`prodata_kubernetes_flavors`](../data-sources/kubernetes_flavors.md) data source. Minimum `1`. Mutually exclusive with `control_plane_size`; when you set `control_plane_size` instead, this is resolved for you and exported as a computed value. Changing it forces a new resource: resizing the control plane in place is not yet supported, so a different master flavor recreates the cluster.
- `region` (String) Region ID. If omitted, uses the provider's default. See the note above about how the create region is resolved. Changing this forces a new resource.
- `project_tag` (String) Project tag the cluster belongs to. If omitted, uses the provider default. Changing this forces a new resource.
- `high_availability` (Boolean) Highly-available control plane (multiple master nodes). Defaults to `false`. Changing it forces a new resource.
- `public_endpoint_enabled` (Boolean) Provision a public IP for the cluster API endpoint. Defaults to `false`. Changing it forces a new resource.
- `ssh_access_enabled` (Boolean) Authorize `public_key` for SSH access to the nodes. Defaults to `false`. Changing it forces a new resource.
- `public_key` (String) SSH public key authorized on the nodes (used when `ssh_access_enabled` is true). Write-once; changing it forces a new resource. A value added to a cluster that was created without one is accepted but never sent to the platform; see [Credentials in Terraform state](#credentials-in-terraform-state).
- `node_ip_range` (String) Control-plane IP range within the local network, as `start-end` (e.g. `10.0.0.10-10.0.0.20`). When omitted, the platform auto-allocates a free contiguous range from `network_id` (sized for the cluster's master and worker capacity) and reports it back; this attribute is then `Computed`. When set, the value is used as-is, but it is validated: it must be an IPv4 `start-end` with `start` below `end`, and — checked against `network_id` before the cluster is created — it must lie inside the network's CIDR and must not contain the network's gateway. A range that includes the network or broadcast address only produces a warning. Changing it forces a new resource.
- `exclude_credentials_from_state` (Boolean) Keep the cluster's credentials out of Terraform state. When `true`, `kube_config` and `private_key_encoded` are always null; read the kubeconfig with the [`prodata_kubernetes_kubeconfig`](../ephemeral-resources/kubernetes_kubeconfig.md) ephemeral resource instead (Terraform 1.10 or later). Unset or `false`, the credentials are stored in state as before. Turning it on removes the credentials from the state written from then on; earlier state versions kept by your backend still contain them. See [Credentials in Terraform state](#credentials-in-terraform-state). Changing it is an in-place update that changes nothing on the cluster. Like any in-place update, it plans `kube_config` and `private_key_encoded` as known after apply — as null when you turn the flag on — so leave the argument unset unless you mean to turn it on.
- `timeouts` (Object) See [Timeouts](#timeouts) below.

### Attribute Reference

- `id` (Number) Cluster ID assigned by the panel.
- `kube_config` (Object, Sensitive) Structured cluster credentials parsed from the kubeconfig, for wiring the `kubernetes` / `helm` providers directly. Null until the kubeconfig is available — the panel fetches it lazily, usually at or shortly after `SUCCESS`; if it still lags, `terraform apply` finishes with a warning and `terraform refresh` populates it. Always null when `exclude_credentials_from_state` is `true`. The certificate fields are base64-encoded exactly as they appear in the kubeconfig — wrap them in `base64decode()`. Attributes:
  - `host` (String) Kubernetes API server URL.
  - `cluster_ca_certificate` (String) Base64-encoded cluster CA certificate.
  - `client_certificate` (String) Base64-encoded client certificate.
  - `client_key` (String) Base64-encoded client key.
  - `token` (String) Bearer token, when the cluster uses token auth (empty otherwise).
  - `raw_config` (String) The full kubeconfig as plain YAML.
- `api_endpoint` (String) Kubernetes API server endpoint.
- `ssh_key_encoded` (String) Base64-encoded SSH public key registered on the nodes. It is the public half, not a secret.
- `private_key_encoded` (String, Sensitive) Base64-encoded SSH private key for the nodes. It exists only when the platform generated the key pair — `ssh_access_enabled` is `true` and `public_key` is not set — and is null when you supplied your own `public_key`. Always null when `exclude_credentials_from_state` is `true`.
- `status` (String) Lifecycle status: `NEW`, `PROCESSING`, `SUCCESS`, `FAIL`, `DELETING`, or `DELETED`. `DELETING` is a lingering state while the cluster's asynchronous teardown runs; on a teardown timeout the cluster goes `FAIL` and keeps reserving its name until it is deleted.
- `blocked` (Boolean) True while a mutating operation is in flight on the cluster.
- `node_pool_count` (Number) Number of node pools on the cluster (master + worker pools). Managed workers are separate `prodata_kubernetes_node_pool` resources.
- `worker_node_count` (Number) Total worker node count across pools.
- `master_node_count` (Number) Master node count.
- `ip_addresses_count` (Number) Number of IP addresses allocated to the cluster.
- `date_created` (String) Server-reported creation timestamp.

### Timeouts

```terraform
resource "prodata_kubernetes_cluster" "example" {
  # ...

  timeouts = {
    create = "90m"
    update = "60m"
    delete = "5m"
  }
}
```

- `create` (String) Default `90m`.
- `update` (String) Default `60m`.
- `delete` (String) Default `5m`.

The provider polls the cluster status every 30s during long-running operations; the timeout bounds the total wait.

## Import

Clusters are imported by their numeric ID, scoped to the provider's default region and project:

```shell
terraform import prodata_kubernetes_cluster.example 42
```

To import a cluster in a different region or project, use the composite form `{region}/{id}@{project_tag}`:

```shell
terraform import prodata_kubernetes_cluster.example UZ-5/42@my-project
```

The cluster imports its control plane only. Import each worker pool separately as a `prodata_kubernetes_node_pool` (see that resource's Import section). The write-once inputs (`network_id`, `public_key`, `ssh_access_enabled`) are not returned by the API — set them in your configuration after import to match the live cluster so the next plan does not force a replacement. `node_ip_range` is read back from the API on import, so it does not need to be re-supplied.

An import stores the cluster's credentials in the state it writes, because `exclude_credentials_from_state` is not something the API returns. With the flag set in your configuration, the first `terraform apply` after the import removes them (an in-place update that changes nothing on the cluster). See [Credentials in Terraform state](#credentials-in-terraform-state).

## Credentials in Terraform state

By default the cluster resource stores two secrets in Terraform state: `kube_config` (a client certificate and key for cluster-admin access, or a token, and the full kubeconfig) and, when the platform generated the nodes' SSH key pair, `private_key_encoded` (the private key that gives SSH access to the nodes). `Sensitive` only hides them from console output; the state file contains them in plaintext, and anyone who can read the state can use them. Kubernetes cannot revoke a client certificate.

Set `exclude_credentials_from_state = true` to keep both out of the state:

- `kube_config` and `private_key_encoded` are always null — after create, update and refresh alike. Nothing else changes: the cluster is created and updated as before, and `terraform apply` still waits for the kubeconfig. (An import is the exception; see [Import](#import).) The flag is also available on the [`prodata_kubernetes_cluster` data source](../data-sources/kubernetes_cluster.md), which would otherwise store the same values.
- Read the kubeconfig with the [`prodata_kubernetes_kubeconfig`](../ephemeral-resources/kubernetes_kubeconfig.md) ephemeral resource, which holds it in memory for the run only. It needs Terraform 1.10 or later; the flag itself works with any Terraform version, but on an older one Terraform has no way to read the kubeconfig.
- Changing the flag is an in-place update that only rewrites the state. It changes nothing on the cluster and does not depend on the cluster's status, so it works for a cluster in the `FAIL` state, or with an operation in flight, as well. Turning the flag off again stores the credentials in the state on the next apply. The flag has no default: leaving it out is the same as `false`, and adding an explicit `exclude_credentials_from_state = false` to an existing cluster is a one-time in-place update of the same kind. Like any in-place update of the cluster, it plans `kube_config` and `private_key_encoded` as known after apply (as null when you turn the flag on), so anything configured from `kube_config` — a `kubernetes` provider, say — receives unknown values in that plan. Leave the argument out, or set it to `null` (in a module, give its variable `default = null`), unless you are turning the exclusion on.

**Use your own SSH key when you create the cluster.** A private key exists only when the platform generates the key pair, which it does when `ssh_access_enabled` is `true` and no `public_key` is given (the platform uses a `public_key` only if it is longer than 5 bytes). Terraform receives that private key only as `private_key_encoded`, so with the flag on you never get it from Terraform. Set `public_key` to authorize a key you hold: the platform then generates nothing, there is no private key to keep out of the state, and you keep access to the nodes. When you create a cluster with the flag on and the platform would generate the key pair, `terraform plan` warns. `ssh_key_encoded` is the public half and is not a secret.

**An existing cluster's SSH key cannot be changed.** The key is fixed when the cluster is created. A `public_key` added later to a cluster that was created without one is accepted by Terraform, but it is not sent to the platform and authorizes nothing. So when you turn the flag on for a cluster whose key pair the platform generated, you remove from the state the only copy of the private key that Terraform holds: copy it out first if you need SSH access to the nodes (for example from `terraform show -json`). The platform keeps the key pair itself, and a data source or a resource without the flag returns it again.

**Earlier state versions keep the credentials.** Turning the flag on removes them from the state Terraform writes from then on. The previous version still contains them — the local `terraform.tfstate.backup`, the versions a remote backend retains, and any copy of the state in backups or CI artifacts. If someone who should not have cluster access could read an earlier version, treat its credentials as exposed: Kubernetes cannot revoke a client certificate, and the nodes' SSH key is fixed when the cluster is created, so exposed key material can be retired only by recreating the cluster.

The flag covers these two attributes only. Other sensitive values the provider stores are listed on the [provider page](../index.md#sensitive-data-and-terraform-state).

## Migrating from a version with `default_node_pool`

Before this release the cluster carried an inline `default_node_pool`. That attribute is removed; the pool it described is now a standalone `prodata_kubernetes_node_pool`. Existing clusters migrate **without destroying worker nodes** — import the pool, never recreate it:

1. Upgrade the provider.
2. Remove the `default_node_pool` block from the cluster's configuration (a stale block now fails at plan with *"Unsupported argument"*).
3. Add a `prodata_kubernetes_node_pool` resource for the existing pool.
4. Import it with the **scoped** ID form — the pool id is the cluster's lowest-id worker pool:
   `terraform import prodata_kubernetes_node_pool.<name> {region}/{cluster_id}/{pool_id}@{project_tag}`
5. `terraform state show prodata_kubernetes_node_pool.<name>` and copy `name`, `vcpu`, `ram`, `disk_size`, `cluster_id`, `region`, `project_tag` verbatim into configuration.
6. **`terraform plan` must report no changes** before any apply. Any replace/in-place diff on the pool means a mismatched field — stop and fix it.

If the original pool was autoscaling, declare `autoscaling { min_nodes, max_nodes }` (values from `state show`) and **omit `node_count`** — copying a literal `node_count` disables the autoscaler and can scale nodes away. If the cluster already had extra `prodata_kubernetes_node_pool` resources, leave them as-is and import only the former default (the lowest-id) pool — never point two resources at one pool id.

## Known Limitations

- **`pod_cidr` is not auto-allocated.** It must be specified explicitly. The node IP range (`node_ip_range`) is auto-allocated from `network_id` when omitted, but the local network must still have enough free contiguous addressing for the cluster's master and worker capacity — creation fails if it does not.
- **`kube_config` is populated lazily.** The kubeconfig is fetched server-side after the cluster reaches `SUCCESS` and can lag briefly; `terraform apply` waits a bounded grace period for it. Gate any downstream consumer (a `kubernetes`/`helm` provider) on `status`.
- **The credentials are stored in state unless you opt out.** `kube_config` and `private_key_encoded` are written to Terraform state by default, and state versions written earlier keep them after you opt out. See [Credentials in Terraform state](#credentials-in-terraform-state).
- **A `FAIL`ed cluster cannot be upgraded.** Inspect it in the panel and recreate it. (An in-place update that does not change `kubernetes_version` — the flag, `timeouts`, or a write-once input set after an import — does not touch the cluster and still works.)
