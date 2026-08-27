---
page_title: "prodata_kubernetes_cluster Resource - ProData Provider"
subcategory: "Kubernetes"
description: |-
  Manages a ProData Managed Kubernetes cluster's control plane; worker pools are managed as independent prodata_kubernetes_node_pool resources.
---

# prodata_kubernetes_cluster (Resource)

Manages a ProData Managed Kubernetes cluster's **control plane**. Worker capacity is managed as independent [`prodata_kubernetes_node_pool`](kubernetes_node_pool.md) resources — including the first pool. A cluster with zero node pools is a valid, control-plane-only steady state.

Cluster creation is asynchronous; `terraform apply` blocks until the cluster reaches `SUCCESS` (with a bounded grace period for the lazily-fetched kubeconfig) or the create timeout elapses. If the kubeconfig still lags after the grace, the apply finishes with a warning and `terraform refresh` populates it.

~> **Note:** The cluster is created in the region the API resolves for the request. If your account spans multiple regions, set the region through the provider configuration (or `PRODATA_REGION`) to be sure the cluster lands where you intend. `region` and `project_tag` are fixed at create time — changing either forces a new resource.

~> **Note:** The networking inputs are immutable — changing `network_id`, `pod_cidr`, or `node_ip_range` forces a new resource. `network_id` is additionally **write-once**: the API does not return it, so it is preserved in state and accepted from configuration after `terraform import` without forcing a replacement. `pod_cidr` and `node_ip_range` are read back normally — `node_ip_range` is `Optional`/`Computed`, so when you omit it the platform auto-allocates a free range from `network_id` and records it in state.

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
  node_ip_range      = "10.0.1.10-10.0.1.20" # explicit range (optional)
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
- `public_key` (String) SSH public key authorized on the nodes (used when `ssh_access_enabled` is true). Write-once; changing it forces a new resource.
- `node_ip_range` (String) Control-plane IP range within the local network, as `start-end` (e.g. `10.0.0.10-10.0.0.20`). When omitted, the platform auto-allocates a free contiguous range from `network_id` (sized for the cluster's master and worker capacity) and reports it back; this attribute is then `Computed`. When set, the value is used as-is. Changing it forces a new resource.
- `timeouts` (Object) See [Timeouts](#timeouts) below.

### Attribute Reference

- `id` (Number) Cluster ID assigned by the panel.
- `kube_config` (Object, Sensitive) Structured cluster credentials parsed from the kubeconfig, for wiring the `kubernetes` / `helm` providers directly. Null until the kubeconfig is available — the panel fetches it lazily, usually at or shortly after `SUCCESS`; if it still lags, `terraform apply` finishes with a warning and `terraform refresh` populates it. The certificate fields are base64-encoded exactly as they appear in the kubeconfig — wrap them in `base64decode()`. Attributes:
  - `host` (String) Kubernetes API server URL.
  - `cluster_ca_certificate` (String) Base64-encoded cluster CA certificate.
  - `client_certificate` (String) Base64-encoded client certificate.
  - `client_key` (String) Base64-encoded client key.
  - `token` (String) Bearer token, when the cluster uses token auth (empty otherwise).
  - `raw_config` (String) The full kubeconfig as plain YAML.
- `api_endpoint` (String) Kubernetes API server endpoint.
- `ssh_key_encoded` (String) Base64-encoded SSH public key registered on the nodes.
- `private_key_encoded` (String, Sensitive) Base64-encoded SSH private key for the nodes.
- `status` (String) Lifecycle status: `NEW`, `PROCESSING`, `SUCCESS`, `FAIL`, or `DELETED`.
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
- **A `FAIL`ed cluster cannot be modified.** Inspect it in the panel and recreate it.
