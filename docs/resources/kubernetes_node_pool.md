---
page_title: "prodata_kubernetes_node_pool Resource - ProData Provider"
subcategory: "Kubernetes"
description: |-
  Manages an additional worker node pool on a ProData Managed Kubernetes cluster.
---

# prodata_kubernetes_node_pool (Resource)

Manages an additional worker node pool on a ProData Managed Kubernetes cluster. The cluster's first (default) worker pool is managed inline by the [`prodata_kubernetes_cluster`](kubernetes_cluster.md) resource; use this resource for every pool beyond it.

Pool creation and scaling are asynchronous; `terraform apply` blocks until the pool converges or the timeout elapses.

~> **Note:** Sizing (`vcpu`, `ram`, `disk_size`) and `name` are immutable — changing them forces a new resource. Only `node_count` and `autoscaling` are updated in place.

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
- `vcpu` (Number) vCPUs per worker node. Minimum `1`. Changing it forces a new resource.
- `ram` (Number) RAM per worker node, in GB. Minimum `1`. Changing it forces a new resource.
- `disk_size` (Number) Disk size per worker node, in GB. Minimum `1`. Changing it forces a new resource.

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

- **The last worker pool cannot be deleted.** The backend refuses to delete a cluster's last worker node pool (it would leave the cluster with no workers). Destroy the whole cluster instead, or add another worker pool first. The provider surfaces this as a clear error.
- **Master (control-plane) pools are not manageable here.** This resource manages worker pools only; the master pool is owned by the cluster's `master_flavor_id`.
