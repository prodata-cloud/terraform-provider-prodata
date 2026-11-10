---
page_title: "prodata_kubernetes_node_pool Resource - ProData Provider"
subcategory: "Kubernetes"
description: |-
  Manages a worker node pool on a ProData Managed Kubernetes cluster.
---

# prodata_kubernetes_node_pool (Resource)

Manages a worker node pool on a ProData Managed Kubernetes cluster. The cluster itself is control-plane-only; **every** worker pool — including the first — is managed with this resource.

Pool creation and scaling are asynchronous; `terraform apply` blocks until the pool converges or the timeout elapses.

Creation and deletion require a panel and Kubernetes service supporting `pool-lifecycle-v1`. The provider records `request_uuid` before reserving a pool and checkpoints its canonical ID before submitting physical work. A lost response resumes that exact operation; an existing pool is never adopted by its name or by comparing list results. A same-name conflict requires explicit import.

Deletion records a separate `deletion_request_uuid` and uses the canonical pool ID with a UID-pinned operation. A missing legacy response, HTTP404, or unverified read preserves Terraform ownership. Only positive cancellation or confirmed deletion of that exact pool removes it from state. `Read` does not submit operations. A temporary projection read failure after confirmed creation keeps the new pool instead of tainting it for replacement.

~> **Note:** A configured `project_tag` (or provider default) must match the parent cluster's project. The provider checks the cluster listing in that selected project before creation and fails if the parent is absent or the listing cannot be read. If neither the resource nor provider selects a project, the panel preserves inheritance from the parent cluster; no project is guessed from a failed read.

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

- `id` (Number) Canonical node pool ID committed by the panel before physical submission. It may remain null after an unacknowledged reservation; the creation UUID is retained.
- `request_uuid` (String) Durable creation request UUID. Computed; do not configure it.
- `deletion_request_uuid` (String) Durable deletion request UUID while a destroy attempt is pending. Computed; retained after an uncertain response.
- `status` (String) Lifecycle status. Pending or unverified operations retain their identity; `SUCCESS` means converged.
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

## Recovery

A pending operation retains its original UUID and canonical ID. Known-UUID submit can resume a pre-submission refusal; it does not replay a raw physical create/delete. A cancelled CREATE with no physical pool can be released, including when the original response never supplied an ID. A cancelled DELETE leaves the live pool owned and permits a fresh destroy attempt.

Imports read a committed creation association when the panel supplies it. Older pools retain their explicit canonical ID and still require a positive UID association for deletion. Unknown responses remain inspectable and never cause name-based adoption or inferred absence.

Terraform stores the provider's response when an RPC finishes. If the provider process exits before that response can be stored, the panel's committed record still owns the pending pool; inspect that canonical pool and import its explicit ID. Do not use a same-name resource as evidence that a new create owns it.

## Known Limitations

- **Deleting the last worker pool is allowed** — a control-plane-only cluster is a valid state. Servers without the durable lifecycle contract fail capability discovery before new allocation; deploy the compatible panel and Kubernetes service before using creation/deletion.
- **Master (control-plane) pools are not manageable here.** This resource manages worker pools only; the master pool is owned by the cluster's `master_flavor_id`.
