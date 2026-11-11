---
page_title: "prodata_vm_gateway Resource - ProData Provider"
subcategory: "Compute"
description: |-
  Manages the IPv4 default gateway of a VM on a local network interface.
---

# prodata_vm_gateway (Resource)

Manages the VM's IPv4 default gateway on one of its attached local networks. The network can be the
VM's primary network or an additional `prodata_local_network_attachment`.

A VM has one IPv4 default route. Use **only one `prodata_vm_gateway` per VM**. Setting this gateway
moves any existing default route from the other interface, including a public interface; that can
change the VM's connectivity. Confirm the intended route before applying.

## Example Usage

```terraform
resource "prodata_vm_gateway" "local" {
  vm_id      = prodata_vm.example.id
  network_id = prodata_local_network_attachment.example.network_id
  gateway    = "10.20.0.1"
}
```

Referencing the attachment's `network_id` also tells Terraform to create the attachment first. For
the main interface, use `prodata_vm.example.local_network_id` instead.

When removing a public IP and moving the default route to a local interface, do this in two applies:
first detach the public IP, then configure the local gateway. Check the route and connectivity
between the two operations; removing a public IP does not automatically restore a local gateway.

Changing the gateway on a running VM **stops and starts the VM**. The platform's stop may power off
the VM immediately and is not a clean guest shutdown. A VM already stopped stays stopped. The
provider first checks the request and does not stop the VM if that check fails. Configuring the
hypervisor's cloud-init settings does not prove the guest operating system applied them; on a VM
without cloud-init, configure the guest separately.

Automatic restart requires matching APIs that confirm recoverable STOP and START operations, independently of guest-agent support. An unchanged route or a VM that stays stopped does not require a new restart. New guest automation additionally checks recoverable power-child support; existing verified reads and exact owned-route cleanup retain their recovery path.

Depending on the image and cloud-init configuration, changing network data may be treated as a new
instance. SSH host keys can be regenerated and once-per-instance scripts can run again. If a
restart cannot be confirmed, apply returns an error and records `restart_pending`. Recover the VM
and apply again; if Terraform tainted a newly created gateway resource, first run `terraform untaint`
for it. Deletion is blocked while restart recovery is pending.
If the resource was removed from configuration or a replacement was requested, restore its
previous configuration and apply to recover it before deleting or replacing it.

The gateway must be a usable IPv4 address in the selected interface's subnet. The platform refuses
a change when the VM or its configuration is locked, when another operation is in progress, or when
there are pending hypervisor configuration changes. HA VMs can be changed once HA has confirmed the
VM is stopped and its requested state is also stopped.

Destroying the resource conditionally clears **only the gateway it last applied or imported**, on
the same interface. If another actor changed the gateway, the new route is left alone. Clearing the
last route can leave the VM without a default gateway; configure the next route as needed. If the
interface was replaced, the provider refuses to change it until the resource is reviewed and
re-imported or recreated.

Gateway changes require `vm-gateway-operation-v1` on the panel and the matching Core protocol.
The provider checkpoints `platform_operation_uuid` before the first reservation request. Reservation
is metadata only; physical submission uses that exact UUID. Lost panel/Core responses are recovered
by observing the same operation. A pending or unavailable result retains ownership and can block
starting or changing the VM. A missing physical Proxmox acknowledgement remains unverified and may
need support to establish that the original writer has terminated. The VM may remain stopped.
Cancellation releases an intent only after a positive no-submission acknowledgement; it never clears
an existing gateway. Do not assume that a timed-out request did nothing.

If the panel reports that the network settings service is busy (error 370971), wait briefly and retry.

## Optional guest configuration

Set `configure_guest = true` to persistently configure and verify the guest's IPv4 address and default route after the platform change. Support is checked before stopping the VM or changing its gateway. This requires the running Linux/root QEMU guest agent and static Netplan/systemd-networkd layout described in [Configuring the VM](local_network_attachment.md#configuring-the-vm). Unsupported guests, including pfSense/FreeBSD and guests without an agent, require manual guest configuration and fail preflight. No packages are installed and no default route is inferred.

The guest MAC, IPv4 address and planned route are checked by a full dry-run before stopping the VM or changing its platform gateway. A persisted guest UUID authorizes only the matching gateway and power-cycle children, and guest execution begins after their verified completion. Only this explicit option authorizes moving the guest IPv4 default route; other interfaces' addresses, IPv6 and DNS remain intact. `guest_configuration_applied` records verified success. External guest drift is corrected in place, without a new interface or a power cycle when the platform gateway is already correct. Disabling the option returns cloud-init ownership while retaining the runtime configuration. Cloud-init stays enabled during normal managed operations.

On destroy, the platform gateway is conditionally cleared first. Guest cleanup then removes only the previously owned IPv4 gateway on the same verified interface, leaving a replacement or foreign route alone. A guest failure retains state and reports an error; if a failed create was tainted, review and untaint the resource before applying again. A timed-out guest request has an unverified outcome and can continue for a bounded period under a guest lock. The original guest file is backed up and restoration is attempted after an apply/readback failure; restoration itself is not guaranteed.

```terraform
resource "prodata_vm_gateway" "managed_guest" {
  vm_id           = prodata_vm.example.id
  network_id      = prodata_local_network_attachment.example.network_id
  gateway         = "192.0.2.1"
  configure_guest = true
}
```

## Schema

### Required

- `vm_id` (Number) ID of the virtual machine. Changing it replaces this resource.
- `network_id` (Number) ID of an attached local network. Changing it replaces this resource.
- `gateway` (String) IPv4 address of the default gateway in the selected interface's subnet.

### Optional

- `region` (String) Region override. Defaults to the provider's region. Changing it replaces this resource.
- `project_tag` (String) Project override. Defaults to the provider's project. Changing it replaces this resource.
- `configure_guest` (Boolean) Default `false`. Explicitly enable supported persistent guest address and IPv4 default-route configuration and drift reconciliation. This option can be enabled in place.
- `timeouts` (Object) Operation ceilings. `create`, `update` and `delete` default to `15m`. A change on a running VM needs at least 13 minutes of remaining time for the stop, the change and the reserved restart time; use a larger value for a slow VM.

### Attribute Reference

- `platform_operation_uuid` (String) Caller-known platform operation, retained on errors for exact recovery or cancellation.
- `platform_operation_status` (String) `RESERVED`, `SUBMITTING`, `SUCCESS`, `NOT_SUBMITTED` or an exact VM-deletion proof `DELETED`.
- `guest_operation_uuid` (String) Persisted guest operation associated with the planned gateway change.
- `guest_operation_status` (String) Last observed guest operation status.
- `guest_owned_gateway` (String) Last gateway verified inside the guest, retained independently during an incomplete platform update.
- `interface_id` (Number) ID of the selected, verified local interface.
- `owned_gateway` (String) Gateway last applied or imported by this resource, used for conditional deletion.
- `restart_pending` (Boolean) A gateway change stopped a running VM but its restart was not confirmed.
- `guest_configuration_applied` (Boolean) Whether opted-in guest address/default-route configuration was verified. False when disabled or verified drift is observed. Unsupported, stopped, busy or unavailable observations preserve the last verified value with a warning.

## Import

Import the gateway of a local network on a VM using `<vm_id>:<network_id>`:

```shell
terraform import prodata_vm_gateway.local 123:456
```

Import adopts the gateway currently present on the selected interface. If there is no gateway on
that interface, the next apply can set the value in the configuration.

Imports and older states default `configure_guest` to `false`; setting it to `true` afterwards performs support checks and reconciles the guest in place.

After a provider process crash before its response reaches Terraform, use the panel's scoped
`GET /api/v2/vms/{vm_id}/gateway-operations/pending?networkId={network_id}` inventory. Explicitly import
a retained SET intent by its UUID:

```shell
terraform import prodata_vm_gateway.local '123:456:11111111-1111-4111-8111-111111111111'
```

This imports that operation's proven ownership and preserves a different current route. A pending
intent remains pending; import performs no write. Resolve a retained conditional CLEAR by its UUID
through the API before importing a current route. Terraform persists provider response state after
the RPC returns, so an abrupt process death before that return cannot guarantee the local checkpoint.
