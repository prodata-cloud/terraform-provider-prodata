---
page_title: "prodata_lb Resource - ProData Provider"
subcategory: "Load Balancer"
description: |-
  Manages a ProData L4 load balancer (TCP/UDP) backed by hidden nginx VMs.
---

# prodata_lb (Resource)

Manages a ProData layer-4 load balancer (TCP or UDP). The platform provisions two hidden nginx VMs in the target local network to back the LB; these VMs are not directly visible or manageable through this provider. Traffic is distributed across the backend VMs in round-robin order.

A balancer can dispatch traffic to either:

- a set of ProData VMs (`backend_group.vm_ids`) — referred to as `FRONTEND` source on the server, or
- an entire Kubernetes node pool (`backend_group.node_pool_id`) — `CCM` source.

An auto scaling group can also supply the backends through `backend_group.asg_id`. The platform keeps its current members synchronized, including when the group scales to zero. Switching between individual VMs and an ASG, or changing the ASG, updates the existing load balancer in place. Switching to or from the CCM node-pool mode requires resource replacement.

An update involving an ASG first verifies that the panel reports `backendSelectorContract = "replace-v1"`. Older APIs are refused before any configure request is sent. The provider supplies a complete selector and explicitly replaces declarations that were left out, so the background sync cannot re-add members of a previous ASG. Upgrade the matching panel API before changing an ASG selector; ordinary VM-only operations remain compatible with older APIs.

~> **Note:** A load balancer backed by `backend_group.node_pool_id` belongs to that pool's cluster project. Its `project_tag` (or the provider default) must match that project. The provider reads the pool and checks that its cluster is listed in the selected project before creating the balancer; a missing parent or an unreadable listing stops creation.

For this node-pool mode, the API key needs Kubernetes **read** permission to verify the parent, in addition to its load-balancer permissions. VM-backed balancers do not perform this Kubernetes lookup.

~> **Note:** Creating a load balancer requires at least **three** free IPs in `network_id` (one VIP plus two for the hidden nginx VMs). The server returns error code 737 ("Insufficient free IPs in network for load balancer.") when the network does not have enough; the provider surfaces this as a clear plan/apply error.

~> **Note:** `name`, `description`, `port`, and `backend_group` content (VM members or an ASG selector) are updated in place. Changing `region`, `project_tag`, `type`, `protocol`, `network_id`, or switching to/from CCM node-pool mode forces a new resource. The hidden nginx VMs are not re-provisioned on in-place updates.

~> **Note:** `description` is **not configurable for CCM (node pool) load balancers.** The panel owns it — it sets `"CCM: <name>"` and ignores any caller-supplied value on both create and update. The provider rejects a user-supplied `description` at plan time to surface the constraint — omit `description` for CCM balancers and let the provider read the panel value back into state.

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
- `backend_group` (Object) Backend selection. Exactly one of `vm_ids`, `asg_id` or `node_pool_id` must be set:
  - `asg_id` (Number, optional) Auto scaling group ID. The group must belong to the balancer's network and project. Changing the group updates the balancer in place. Requires an API that reports durable `backendGroups` declarations; the provider verifies the requested declaration before reporting success.
  - `vm_ids` (Set of String, optional) Set of VM guids — the `guid` attribute on `prodata_vm`. At least one entry required when this mode is used. Re-ordering produces no diff. Used by `FRONTEND`-source LBs.

~> **Note:** `vm_ids` expects VM **guids** — the computed `guid` attribute of `prodata_vm` (e.g. `prodata_vm.web[*].guid`), **not** the numeric `prodata_vm.id`. Passing the id (or any non-guid value) fails the create with a backend-not-found error.
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
- `backend_selector_drift` (Boolean) Whether the server reports mixed or unsupported frontend selectors. Refresh warns and retains ownership; the next apply reconciles the configured complete selector in place, including extra declarations whose groups have no current members.

### Timeouts

Configure operation timeouts via the optional `timeouts` attribute. Defaults are tuned to typical nginx VM provisioning latency:

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

Load balancers are imported using their numeric ID, scoped to the provider's default region and project:

```shell
terraform import prodata_lb.example 42
```

To import an LB that lives in a different region or project, use the composite form `{region}/{id}@{project_tag}`:

```shell
terraform import prodata_lb.example UZ-5/42@my-project
```

With the bare-ID form, `region` and `project_tag` are seeded from the provider defaults; if the LB lives elsewhere, either use the composite form or set them explicitly in your configuration before the next `terraform plan` so the read scopes correctly.

~> **Note:** For an imported `CCM` load balancer, the provider reads `node_pool_id` from a durable pool declaration or one positively tagged backend pool. Set HCL to that verified pool. An imported null pool value does not itself force replacement. If the API cannot confirm an association, refresh warns and destroy remains available, while a plan that adds a pool stops with an explicit error and preserves the existing balancer. The provider never guesses from VM names or pool names.

## Known Limitations

- **IP auto-allocation is API-key only.** Creates that omit explicit IPs allocate a VIP and the two nginx backend IPs from the network's free pool — this is enabled for API-key callers (i.e. Terraform) but not for JWT/UI callers. If the network has fewer than three free IPs the create fails with server error 737.
- **`getFreeNetIps` is a snapshot.** Two near-simultaneous LB creates against the same near-full network can both be told the same free IPs, racing for the same VIP. The platform's resolution path is being hardened; for now, serialize LB creates against tight networks (`depends_on` between resources, or `terraform apply -parallelism=1`) when free IP capacity is close to the minimum.
- **Switching `node_pool_id` requires destroy+recreate.** The panel's configure endpoint has no `nodePoolId` parameter, so the provider gates pool swaps via `RequiresReplace`. Same-pool configures (rename, port edits) are applied in place as expected.
- **A `backend_group` that is unknown as a whole at plan time.** When the whole block depends on a value known only after apply (for example a conditional on another resource's attribute), the plan cannot tell whether the backend mode or `node_pool_id` of an existing balancer will change. If one of them does, `terraform apply` stops with `Provider produced inconsistent final plan` before touching the balancer; run `terraform apply` again and the new plan shows the replacement. A block that is known but has an unknown member (`node_pool_id = prodata_kubernetes_node_pool.pool.id`) is not affected.
- **CCM association requires server evidence.** Legacy backends without a pool tag and empty CCM rosters without a durable pool declaration cannot establish an imported pool identity. Existing known state is retained, refresh warns, and an unverified import cannot automatically plan replacement. See the Import note.
- **`description` is panel-controlled for CCM balancers.** The provider rejects a user-supplied `description` at plan time for CCM (node pool) balancers, on both create and update; the panel sets it to `"CCM: <name>"` and that value reads back into state. Frontend (VM-backed) LBs accept user-supplied descriptions normally.
- **`date_created` is preserved across updates.** The provider retains the prior creation timestamp when handling configure responses, including responses from legacy API versions.
- **Legacy LBs may report empty `source`.** Load balancers created before source tracking landed can read `source = ""`. The provider uses the frontend update and delete endpoints for this legacy value.
- **Hidden nginx VMs.** Each LB creates two nginx VMs in `network_id`. They are managed by the platform and do not appear in `prodata_vms`, but they do consume VM quota — plan capacity accordingly. They are cleaned up on `terraform destroy`.
- **No TLS termination.** This LB is L4 only. There is no HTTPS / SSL termination, no per-listener protocol override, no session stickiness, no health-check tuning, no tags, and no equivalent of `force_destroy`. These are not on the v0 roadmap.


Refresh and destroy recognize only the LB API's explicit `736` absence code or a matching-ID DELETED response. A proxy `404` response and missing-resource codes for other resource types retain state and surface an error. Temporary selector-lock refusal can be retried only when the API supplies its explicit safe-retry proof; an ambiguous `806` AWX launch error is never automatically replayed.
