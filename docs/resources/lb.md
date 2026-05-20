---
page_title: "prodata_lb Resource - ProData Provider"
description: |-
  Manages a ProData L4 load balancer (TCP/UDP) backed by hidden HAProxy VMs.
---

# prodata_lb (Resource)

Manages a ProData layer-4 load balancer (TCP or UDP). The platform provisions two hidden HAProxy VMs in the target local network to back the LB; these VMs are not directly visible or manageable through this provider.

A balancer can dispatch traffic to either:

- a set of ProData VMs (`backend_group.vm_ids`) — referred to as `FRONTEND` source on the server, or
- an entire Kubernetes node pool (`backend_group.node_pool_id`) — `CCM` source.

Switching between the two backend modes forces resource replacement; same-mode content changes (renaming, port edits, swapping VM members in/out, adjusting `description`) are applied in place.

~> **Note:** Creating a load balancer requires at least **three** free IPs in `network_id` (one VIP plus two for the hidden HAProxy VMs). The server returns error code 737 ("Insufficient free IPs in network for load balancer.") when the network does not have enough; the provider surfaces this as a clear plan/apply error.

~> **Note:** `name`, `description`, `port`, and `backend_group` content (VM members) are updated in place. Changing `region`, `project_tag`, `type`, `protocol`, `network_id`, or switching backend modes forces a new resource. The hidden HAProxy VMs are not re-provisioned on in-place updates.

~> **Note:** `description` is **not configurable for CCM (node pool) load balancers.** The panel hard-codes it to `"CCM: <name>"` at create time and silently discards any caller-supplied value. The provider rejects the configuration at plan time to surface the constraint — omit `description` for CCM balancers and let the provider read the panel value back into state.

## Example Usage

### External TCP load balancer backed by VMs

```terraform
resource "prodata_lb" "web" {
  name        = "web-lb"
  description = "Production web tier"
  type        = "external"
  protocol    = "TCP"
  network_id  = prodata_local_network.web.id

  port = [
    { port = 443, target_port = 8443 },
    { port = 80, target_port = 8080 },
  ]

  backend_group = {
    vm_ids = [
      prodata_vm.web_1.guid,
      prodata_vm.web_2.guid,
    ]
  }
}
```

### Internal UDP load balancer

```terraform
resource "prodata_lb" "metrics" {
  name       = "metrics-collector"
  type       = "internal"
  protocol   = "UDP"
  network_id = prodata_local_network.metrics.id

  port = [
    { port = 8125, target_port = 8125 },
  ]

  backend_group = {
    vm_ids = [prodata_vm.collector.guid]
  }
}
```

### External load balancer fronting a Kubernetes node pool

```terraform
resource "prodata_lb" "ingress" {
  name       = "ingress-lb"
  type       = "external"
  protocol   = "TCP"
  network_id = data.prodata_local_network.k8s.id

  port = [
    { port = 443, target_port = 30443 },
  ]

  backend_group = {
    node_pool_id = 42
  }
}
```

## Schema

### Required

- `name` (String) Load balancer name. Must be unique within the parent organization and region. Updated in place.
- `type` (String) Load balancer type: `external` (public IP) or `internal` (private VIP only). Changing this forces a new resource.
- `protocol` (String) L4 protocol: `TCP` or `UDP`. Case-sensitive — the provider rejects any other value at plan time (the panel silently downgrades unknown values to TCP, so the validator is strict here). One protocol per balancer; mixed protocols on one LB are not supported. Changing this forces a new resource.
- `network_id` (Number) Local network ID. For VM backends, every VM in `backend_group.vm_ids` must belong to this network. For Kubernetes node pool backends, this must match the cluster's network. Changing this forces a new resource.
- `port` (Set of Object, 1-10 entries) Port mappings. Re-ordering entries does not produce a diff (set semantics). Each entry has:
  - `port` (Number, required) Port exposed on the load balancer (1-65535).
  - `target_port` (Number, required) Port on each backend (1-65535).
- `backend_group` (Object) Backend selection. Exactly one of `vm_ids` or `node_pool_id` must be set:
  - `vm_ids` (Set of String, optional) Set of VM guids — the `guid` attribute on `prodata_vm`. At least one entry required when this mode is used. Re-ordering produces no diff. Used by `FRONTEND`-source LBs.
  - `node_pool_id` (Number, optional) Kubernetes node pool ID. Whole-pool only (no partial backends). Changing the pool itself forces a new resource — see "Known Limitations" for the reason.

### Optional

- `region` (String) Region ID. If omitted, uses the provider's default region. Changing this forces a new resource.
- `project_tag` (String) Project tag the load balancer belongs to. If omitted, uses the provider default. Changing this forces a new resource.
- `description` (String) Free-form description. Updated in place. **Not configurable for CCM load balancers** — see note above.
- `timeouts` (Object) See [Timeouts](#timeouts) below.

### Attribute Reference

- `id` (Number) Load balancer ID assigned by the panel.
- `source` (String) Backend source reported by the panel: `FRONTEND` (VM backends) or `CCM` (Kubernetes node pool).
- `status` (String) Lifecycle status: `NEW`, `PROCESSING`, `SUCCESS`, `DELETED`, or `FAIL`. After a successful `apply`, this will read `SUCCESS`.
- `public_ip` (String) Public IP assigned to external load balancers. Null for internal balancers and transiently null while status is `NEW`.
- `private_ip` (String) Private VIP allocated inside `network_id`. May be transiently null while status is `NEW`.
- `date_created` (String) Server-reported creation timestamp.

### Timeouts

Configure operation timeouts via the optional `timeouts` attribute. Defaults are tuned to typical HAProxy VM provisioning latency:

```terraform
resource "prodata_lb" "example" {
  # ...

  timeouts = {
    create = "30m"
    update = "30m"
    delete = "15m"
  }
}
```

- `create` (String) Default `30m`.
- `update` (String) Default `30m`.
- `delete` (String) Default `15m`.

The provider polls the LB status every 30s during `create`, `update`, and `delete`; the timeout bounds the total wait.

## Import

Load balancers are imported using their numeric ID:

```shell
terraform import prodata_lb.example 42
```

After import, `region` and `project_tag` are seeded from the provider defaults. If the LB lives in a different region/project, set them explicitly in your configuration before the next `terraform plan` so the read scopes correctly.

~> **Note:** A `CCM`-source LB imported with this resource cannot have its `node_pool_id` populated from state — the panel does not surface `node_pool_id` on the GET endpoint. You must set `backend_group.node_pool_id` in your HCL after import to match the actual pool, otherwise the next plan will not be able to manage backend membership and update operations will fail. See "Known Limitations".

## Known Limitations

- **IP auto-allocation is API-key only.** Creates that omit explicit IPs allocate a VIP and the two HAProxy backend IPs from the network's free pool — this is enabled for API-key callers (i.e. Terraform) but not for JWT/UI callers. If the network has fewer than three free IPs the create fails with server error 737.
- **`getFreeNetIps` is a snapshot.** Two near-simultaneous LB creates against the same near-full network can both be told the same free IPs, racing for the same VIP. The platform's resolution path is being hardened; for now, serialize LB creates against tight networks (`depends_on` between resources, or `terraform apply -parallelism=1`) when free IP capacity is close to the minimum.
- **Switching `node_pool_id` requires destroy+recreate.** The panel's configure endpoint has no `nodePoolId` parameter, so the provider gates pool swaps via `RequiresReplace`. Same-pool configures (rename, port edits) are applied in place as expected.
- **`node_pool_id` is not surfaced on GET.** The panel does not return `nodePoolId` in the load-balancer read shape, so the resource preserves it from prior state. After `terraform import`, you must restate it in HCL — see the Import note.
- **`description` is panel-controlled on CCM creates.** The provider rejects a user-supplied `description` at plan time for CCM (node pool) balancers; the panel populates it as `"CCM: <name>"` and that value reads back into state. Frontend (VM-backed) LBs accept user-supplied descriptions normally.
- **`date_created` is preserved across updates.** The panel's configure endpoint resets `dateCreated` to the current time, which would fail Terraform's "computed output must be consistent" check. The provider re-injects the prior `date_created` into state on update so plans stay stable.
- **Legacy LBs may report empty `source`.** Load balancers created before source tracking landed will read `source = ""`. The provider treats empty source as `FRONTEND` for compatibility on `delete` and surfaces a clear error if you try to `update` such an LB — destroy and recreate to migrate it onto the new schema.
- **Hidden HAProxy VMs.** Each LB creates two HAProxy VMs in `network_id`. They are managed by the platform and do not appear in `prodata_vms`, but they do consume VM quota — plan capacity accordingly. They are cleaned up on `terraform destroy`.
- **No TLS termination.** This LB is L4 only. There is no HTTPS / SSL termination, no per-listener protocol override, no session stickiness, no health-check tuning, no tags, and no equivalent of `force_destroy`. These are not on the v0 roadmap.
