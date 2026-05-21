---
page_title: "prodata_lb Data Source - ProData Provider"
subcategory: "Load Balancer"
description: |-
  Look up a single ProData load balancer by ID.
---

# prodata_lb (Data Source)

Look up a single ProData load balancer by ID. Returns the load balancer's full attributes except the Kubernetes node-pool ID — the panel does not surface `nodePoolId` on the GET endpoint, so the data source has no way to expose it.

A non-existent ID surfaces as a clear error (server error code 736), not an empty result.

~> **Note:** Backends are flattened on the data source: `vm_ids` is exposed at the top level rather than nested inside a `backend_group` block (the way the resource does). For `CCM`-source load balancers this set is empty.

## Example Usage

```terraform
data "prodata_lb" "web" {
  id = 42
}

output "lb_public_ip" {
  value = data.prodata_lb.web.public_ip
}

output "lb_backend_vms" {
  value = data.prodata_lb.web.vm_ids
}

output "lb_ports" {
  value = data.prodata_lb.web.port
}
```

## Schema

### Required

- `id` (Number) Load balancer ID to look up.

### Optional

- `region` (String) Region ID override. If omitted, uses the provider's default region.
- `project_tag` (String) Project tag override. If omitted, uses the provider default.

### Attribute Reference

- `name` (String) Load balancer name.
- `description` (String) Free-form description. For `CCM`-source LBs the panel sets this to `"CCM: <name>"` at create time.
- `type` (String) Load balancer type: `external` (public IP) or `internal`.
- `protocol` (String) L4 protocol: `TCP` or `UDP`.
- `network_id` (Number) Local network ID.
- `source` (String) Backend source: `FRONTEND` (VM backends) or `CCM` (Kubernetes node pool).
- `status` (String) Lifecycle status: `NEW`, `PROCESSING`, `SUCCESS`, `DELETED`, or `FAIL`.
- `public_ip` (String) Public IP (external LBs only).
- `private_ip` (String) Private VIP inside `network_id`.
- `date_created` (String) Server-reported creation timestamp.
- `port` (List of Object) Port mappings, in server order. Each entry has:
  - `port` (Number) Port on the load balancer.
  - `target_port` (Number) Port on each backend.
- `vm_ids` (Set of String) Set of VM guids backing the LB. Populated for `FRONTEND`-source LBs; empty for `CCM`-source LBs.

## Known Limitations

- **`node_pool_id` is not exposed.** The panel does not return `nodePoolId` on the load-balancer GET response, so this data source cannot surface it. Track node-pool membership separately through your Kubernetes tooling for `CCM`-source LBs.
- **Legacy LBs may report empty `source`.** Load balancers created before source tracking landed read with `source = ""`. They are still returned by this data source; downstream callers that switch on `source` should handle the empty case.
