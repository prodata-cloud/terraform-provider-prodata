---
page_title: "prodata_lbs Data Source - ProData Provider"
description: |-
  List ProData load balancers visible to the current project.
---

# prodata_lbs (Data Source)

List the load balancers visible to the project resolved from `project_tag` (or the provider default). Returns a summary view — port mappings and backend membership are intentionally omitted to keep list responses small. Use the [`prodata_lb`](./lb.md) data source for the full shape of a single LB.

`DELETED` load balancers are filtered out by the server. Soft-deleted LBs awaiting scheduler cleanup will already be absent from this list, even if their hidden HAProxy VMs have not yet been physically destroyed.

~> **Note:** Like every data source, `prodata_lbs` is read at plan time. If you create a `prodata_lb` and reference `data.prodata_lbs` in the same apply, the data source reads **before** the new LB exists and the list will not include it. To capture same-apply resources in the list, add `depends_on = [prodata_lb.example]` to the data source — the read is then sequenced after the resource. Alternatively, run `terraform refresh` after the apply.

## Example Usage

```terraform
data "prodata_lbs" "all" {}

output "lb_count" {
  value = length(data.prodata_lbs.all.load_balancers)
}

output "lb_names" {
  value = [for lb in data.prodata_lbs.all.load_balancers : lb.name]
}

output "external_lb_public_ips" {
  value = [
    for lb in data.prodata_lbs.all.load_balancers :
    lb.public_ip
    if lb.type == "external" && lb.public_ip != null
  ]
}
```

### Listing after a same-apply create

```terraform
resource "prodata_lb" "new" {
  name       = "fresh-lb"
  type       = "internal"
  protocol   = "TCP"
  network_id = prodata_local_network.app.id

  port = [
    { port = 5432, target_port = 5432 },
  ]

  backend_group = {
    vm_ids = [prodata_vm.db.guid]
  }
}

data "prodata_lbs" "all" {
  depends_on = [prodata_lb.new]
}
```

## Schema

### Optional

- `region` (String) Region ID override. If omitted, uses the provider's default region.
- `project_tag` (String) Project tag override. If omitted, uses the provider default.

### Attribute Reference

- `load_balancers` (List of Object) Load balancers, in the order returned by the server. Each entry has:
  - `id` (Number) Load balancer ID.
  - `name` (String) Load balancer name.
  - `type` (String) Load balancer type: `external` or `internal`.
  - `status` (String) Lifecycle status: `NEW`, `PROCESSING`, `SUCCESS`, `DELETED`, or `FAIL`. (Active list calls do not surface `DELETED` — the server filters it out.)
  - `source` (String) Backend source: `FRONTEND` or `CCM`. Legacy LBs may report empty.
  - `public_ip` (String) Public IP (external LBs only).
  - `private_ip` (String) Private VIP inside the LB's local network.
  - `date_created` (String) Server-reported creation timestamp.

## Known Limitations

- **Summary shape only.** Port mappings and backend VM/pool membership are not included; query [`prodata_lb`](./lb.md) by ID for the full shape of a specific LB.
- **No filtering or sorting parameters.** The list is server-ordered and returned in full. Filter on the Terraform side via `for` expressions if needed.
- **Plan-time read ordering.** As noted above, list reads happen before resource creates in the same apply unless `depends_on` is set.
- **Legacy LBs may report empty `source`.** See the resource doc — pre-source-tracking LBs read with `source = ""`.
