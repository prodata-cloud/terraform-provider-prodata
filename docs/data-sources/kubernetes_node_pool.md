---
page_title: "prodata_kubernetes_node_pool Data Source - ProData Provider"
subcategory: "Kubernetes"
description: |-
  Look up a single node pool within a ProData Managed Kubernetes cluster.
---

# prodata_kubernetes_node_pool (Data Source)

Look up a single node pool within a ProData Managed Kubernetes cluster by `id` or `name` (exactly one is required, in addition to `cluster_id`).

~> **Note:** Unlike the [`prodata_kubernetes_node_pool`](../resources/kubernetes_node_pool.md) resource's nested `autoscaling` block, autoscaling is exposed on the data source as the flat computed attributes `autoscale_enabled` / `min_nodes` / `max_nodes` (`min_nodes` / `max_nodes` are `0` when autoscaling is off).

## Example Usage

```terraform
data "prodata_kubernetes_node_pool" "workers" {
  cluster_id = 42
  name       = "gpu-workers"
}

output "pool_node_count" {
  value = data.prodata_kubernetes_node_pool.workers.node_count
}
```

## Schema

### Required

- `cluster_id` (Number) ID of the cluster the pool belongs to.

Exactly one of `id` or `name` selects the pool; both are also computed, so the one you omit is populated on read.

- `id` (Number) Node pool ID. Conflicts with `name`.
- `name` (String) Node pool name. Conflicts with `id`.

### Optional

- `region` (String) Region ID override. If omitted, uses the provider's default region.
- `project_tag` (String) Project tag override. If omitted, uses the provider default.

### Attribute Reference

- `vcpu` (Number) vCPUs per worker node.
- `ram` (Number) RAM per worker node, in GB.
- `disk_size` (Number) Disk size per worker node, in GB.
- `node_count` (Number) Current number of worker nodes.
- `node_subnet` (Number) Node subnet prefix length assigned to the pool.
- `status` (String) Lifecycle status: `PROCESSING` or `SUCCESS`.
- `autoscale_enabled` (Boolean) Whether the cluster-autoscaler manages this pool.
- `min_nodes` (Number) Autoscaling minimum node count (`0` when autoscaling is off).
- `max_nodes` (Number) Autoscaling maximum node count (`0` when autoscaling is off).
