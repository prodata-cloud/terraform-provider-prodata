---
page_title: "prodata_kubernetes_cluster Data Source - ProData Provider"
subcategory: "Kubernetes"
description: |-
  Look up a single ProData Managed Kubernetes cluster by id or name.
---

# prodata_kubernetes_cluster (Data Source)

Look up a single ProData Managed Kubernetes cluster by `id` or `name` (exactly one is required). Returns the cluster's configuration plus its `kube_config`, API endpoint and counts.

Looking up by `id` can return a soft-deleted cluster, which is rejected with an error; looking up by `name` only sees live clusters and errors on an ambiguous name.

## Example Usage

```terraform
data "prodata_kubernetes_cluster" "main" {
  name = "prod-cluster"
}

output "cluster_status" {
  value = data.prodata_kubernetes_cluster.main.status
}

# Wire the kubernetes provider from the looked-up cluster.
provider "kubernetes" {
  host                   = data.prodata_kubernetes_cluster.main.kube_config.host
  cluster_ca_certificate = base64decode(data.prodata_kubernetes_cluster.main.kube_config.cluster_ca_certificate)
  client_certificate     = base64decode(data.prodata_kubernetes_cluster.main.kube_config.client_certificate)
  client_key             = base64decode(data.prodata_kubernetes_cluster.main.kube_config.client_key)
}
```

## Schema

### Required

Exactly one of `id` or `name` selects the cluster; both are also computed, so the one you omit is populated on read.

- `id` (Number) Cluster ID. Conflicts with `name`.
- `name` (String) Cluster name. Conflicts with `id`.

### Optional

- `region` (String) Region ID override. If omitted, uses the provider's default region.
- `project_tag` (String) Project tag override. If omitted, uses the provider default.

### Attribute Reference

- `kubernetes_version` (String) Kubernetes version (e.g. `v1.31.4`).
- `high_availability` (Boolean) Whether the control plane is highly available.
- `public_endpoint_enabled` (Boolean) Whether the cluster API endpoint has a public IP.
- `pod_cidr` (String) Pod network CIDR.
- `master_flavor_id` (Number) Master node configuration (flavor) ID.
- `api_endpoint` (String) Kubernetes API server endpoint. Null until the cluster reaches `SUCCESS`.
- `kube_config` (Object, Sensitive) Structured cluster credentials parsed from the kubeconfig. Null until the kubeconfig is available (usually at or shortly after `SUCCESS`). The certificate fields are base64 as they appear in the kubeconfig — wrap them in `base64decode()`. Attributes: `host`, `cluster_ca_certificate`, `client_certificate`, `client_key`, `token`, `raw_config`.
- `ssh_key_encoded` (String) Base64-encoded SSH public key registered on the nodes.
- `private_key_encoded` (String, Sensitive) Base64-encoded SSH private key for the nodes.
- `status` (String) Lifecycle status: `NEW`, `PROCESSING`, `SUCCESS`, or `FAIL`. A `DELETED` cluster is never returned — the lookup errors instead.
- `blocked` (Boolean) True while a mutating operation is in flight on the cluster.
- `node_pool_count` (Number) Number of node pools (including the default and master pools).
- `worker_node_count` (Number) Total worker node count across pools.
- `master_node_count` (Number) Master node count.
- `ip_addresses_count` (Number) Number of IP addresses allocated to the cluster.
- `date_created` (String) Server-reported creation timestamp.
