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

If the platform loses the result of a network change, it can block starting or changing the VM
until support has checked the operation. The VM may remain stopped. Do not assume that a timed-out
request did nothing. These features require a panel and proxmox-core version that implement the VM
network-settings API.

If the panel reports that the network settings service is busy (error 370971), wait briefly and retry.

## Schema

### Required

- `vm_id` (Number) ID of the virtual machine. Changing it replaces this resource.
- `network_id` (Number) ID of an attached local network. Changing it replaces this resource.
- `gateway` (String) IPv4 address of the default gateway in the selected interface's subnet.

### Optional

- `region` (String) Region override. Defaults to the provider's region. Changing it replaces this resource.
- `project_tag` (String) Project override. Defaults to the provider's project. Changing it replaces this resource.
- `timeouts` (Object) Operation ceilings. `create`, `update` and `delete` default to `15m`. A change on a running VM needs at least 13 minutes of remaining time for the stop, the change and the reserved restart time; use a larger value for a slow VM.

### Attribute Reference

- `interface_id` (Number) ID of the selected, verified local interface.
- `owned_gateway` (String) Gateway last applied or imported by this resource, used for conditional deletion.
- `restart_pending` (Boolean) A gateway change stopped a running VM but its restart was not confirmed.

## Import

Import the gateway of a local network on a VM using `<vm_id>:<network_id>`:

```shell
terraform import prodata_vm_gateway.local 123:456
```

Import adopts the gateway currently present on the selected interface. If there is no gateway on
that interface, the next apply can set the value in the configuration.
