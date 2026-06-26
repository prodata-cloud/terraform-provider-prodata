---
page_title: "prodata_kubernetes_flavors Data Source - ProData Provider"
subcategory: "Kubernetes"
description: |-
  List the master-node configurations (flavors) available for the region.
---

# prodata_kubernetes_flavors (Data Source)

List the master-node configurations (flavors) available for the resolved region, used for the `master_flavor_id` of a [`prodata_kubernetes_cluster`](../resources/kubernetes_cluster.md). When `high_availability` is omitted, both HA and non-HA flavors are returned.

## Example Usage

```terraform
data "prodata_kubernetes_flavors" "ha" {
  high_availability = true
}

resource "prodata_kubernetes_cluster" "main" {
  master_flavor_id  = data.prodata_kubernetes_flavors.ha.flavors[0].id
  high_availability = true
  # ...
}
```

## Schema

### Optional

- `region` (String) Region ID override. If omitted, uses the provider's default region.
- `project_tag` (String) Project tag override. If omitted, uses the provider default.
- `high_availability` (Boolean) Restrict the result to highly-available (`true`) or single-master (`false`) flavors. If omitted, both are returned.

### Attribute Reference

- `flavors` (List of Object) Master-node flavors. Each entry has:
  - `id` (Number) Flavor ID — use as `master_flavor_id`.
  - `vcpu` (Number) vCPUs per master node.
  - `ram` (Number) RAM per master node, in GB.
  - `disk_size` (Number) Disk size per master node, in GB.
  - `high_availability` (Boolean) Whether this flavor provisions a highly-available control plane.
  - `region_id` (Number) Region this flavor belongs to.
  - `size` (String) Control-plane size class (`small`/`medium`/`large`) derived from this flavor's capacity rank within its HA mode — the value you can pass to `prodata_kubernetes_cluster.control_plane_size`. Empty if this HA mode's catalog is not a clean 3-tier ladder.
