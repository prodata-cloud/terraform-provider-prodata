---
page_title: "prodata_kubernetes_node_pool Resource - ProData Provider"
subcategory: "Kubernetes"
description: |-
  Manages a worker node pool on a ProData Managed Kubernetes cluster.
---

# prodata_kubernetes_node_pool (Resource)

Manages a worker node pool on a ProData Managed Kubernetes cluster. The cluster itself is control-plane-only; **every** worker pool — including the first — is managed with this resource.

Pool creation and scaling are asynchronous; `terraform apply` blocks until the pool converges or the timeout elapses.

~> **Note:** The pool's `project_tag` (or the provider default) must match its parent cluster's project. The provider checks the cluster listing in that project before creating the pool and fails if the parent is absent or the listing cannot be read. Referencing `cluster_id` does not make the pool inherit the cluster's project; configure both resources for the same project.

Sizing (`vcpu`, `ram`, and increases to `disk_size`) updates the existing pool by rolling its worker machines, preserving the pool ID. Workers are drained and replaced one at a time with at most one unavailable worker and no extra surge worker. Local data on worker boot disks is lost. Application data must use storage with suitable retention. Workloads must tolerate a worker restart, and disruption budgets can delay completion. Disk shrinking is rejected. Changing `name`, `cluster_id`, `region`, or `project_tag` still requires a separate pool.

Hardware updates require both a panel and Kubernetes service supporting the versioned worker-hardware API. Older servers fail the update and retain the resource in state. The provider waits for the current MachineDeployment generation and all updated workers to be ready; a database status of `SUCCESS` alone does not complete a hardware update. Use `timeouts.update` to allow time for larger pools to roll.

## Example Usage

```terraform
# A fixed-size worker pool.
resource "prodata_kubernetes_node_pool" "gpu" {
  cluster_id = prodata_kubernetes_cluster.main.id
  name       = "gpu-workers"
  vcpu       = 8
  ram        = 32
  disk_size  = 120
  node_count = 2
}

# An autoscaling worker pool. Omit node_count — the autoscaler owns the count.
resource "prodata_kubernetes_node_pool" "batch" {
  cluster_id = prodata_kubernetes_cluster.main.id
  name       = "batch-workers"
  vcpu       = 4
  ram        = 8
  disk_size  = 80

  autoscaling = {
    min_nodes = 1
    max_nodes = 10
  }
}
```

## Schema

### Required

- `cluster_id` (Number) ID of the cluster this pool belongs to. Minimum `1`. Changing it forces a new resource (a pool cannot be moved between clusters).
- `name` (String) Pool name. 3-24 characters, lowercase letters / digits / hyphens, not starting or ending with a hyphen. Must be unique within the cluster. Changing it forces a new resource.
- `vcpu` (Number) vCPUs per worker node. Minimum `1`. Updated through a worker rollout within the existing pool.
- `ram` (Number) RAM per worker node, in GB. Minimum `1`. Updated through a worker rollout within the existing pool.
- `disk_size` (Number) Disk size per worker node, in GB. Minimum `1`. Increases roll the workers within the existing pool. Shrinking is not supported.

One of `node_count` or `autoscaling` must be set (they are mutually exclusive).

### Optional

- `region` (String) Region ID. If omitted, uses the provider's default. Must match the cluster's region. Changing this forces a new resource.
- `project_tag` (String) Project tag. If omitted, uses the provider default. Changing this forces a new resource.
- `node_count` (Number) Number of worker nodes. Minimum `1`. Updated in place. Must be omitted when `autoscaling` is set — the autoscaler then owns the count, exported as a computed value.
- `autoscaling` (Object) Enable the cluster-autoscaler for this pool. Its presence enables it; omit the block for a fixed-size pool. Mutually exclusive with `node_count`. Attributes:
  - `min_nodes` (Number, required) Minimum node count. Minimum `1`.
  - `max_nodes` (Number, required) Maximum node count. Minimum `1`, and >= `min_nodes`.
- `timeouts` (Object) See [Timeouts](#timeouts) below.

### Attribute Reference

- `id` (Number) Node pool ID assigned by the panel.
- `status` (String) Lifecycle status: `PROCESSING` while a change is rolling out, `SUCCESS` when converged.
- `node_subnet` (Number) Node subnet prefix length assigned to the pool by the backend.

### Timeouts

```terraform
resource "prodata_kubernetes_node_pool" "example" {
  # ...

  timeouts = {
    create = "30m"
    update = "30m"
    delete = "10m"
  }
}
```

- `create` (String) Default `30m`.
- `update` (String) Default `30m`.
- `delete` (String) Default `10m`.

## Import

Node pools are imported with the composite `{cluster_id}/{pool_id}` form:

```shell
terraform import prodata_kubernetes_node_pool.example 42/7
```

To import a pool in a different region or project, use `{region}/{cluster_id}/{pool_id}@{project_tag}`:

```shell
terraform import prodata_kubernetes_node_pool.example UZ-5/42/7@my-project
```

## Known Limitations

- **Deleting the last worker pool is allowed** — a control-plane-only cluster is a valid state. (Against a backend not yet upgraded, the panel still refuses with code 756; the provider surfaces that as a clear message. Destroy the whole cluster, or add another pool first, in that case.)
- **Master (control-plane) pools are not manageable here.** This resource manages worker pools only; the master pool is owned by the cluster's `master_flavor_id`.
