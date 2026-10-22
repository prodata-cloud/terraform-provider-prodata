---
page_title: "prodata_local_network_attachment Resource - ProData Provider"
subcategory: "Networking"
description: |-
  Attaches a virtual machine to one more ProData local network, as an additional network interface.
---

# prodata_local_network_attachment (Resource)

Attaches a virtual machine to one more ProData local network, as an additional network interface. Destroying this resource detaches the interface from the VM.

The VM's main interface, the one in the VM's own network (the network named by `local_network_id` of its `prodata_vm`), is not managed by this resource and cannot be detached. Adding an interface changes neither `prodata_vm.private_ip` nor `prodata_vm.local_network_id`: both keep describing the main interface. To put the VM itself in another network, change `local_network_id` of the `prodata_vm` instead: it moves the main interface in place (see [Moving a VM to another network](vm.md#moving-a-vm-to-another-network)).

~> **Note:** `vm_id`, `network_id`, `ip`, `region` and `project_tag` require resource replacement when changed. Any change will detach the interface and attach it again, with another MAC address and, unless `ip` is set, possibly another address. `reboot_after_attach` and `timeouts` can be changed in place, but only a new attachment makes use of them.

~> **Note:** The platform does not set up the new interface inside the guest operating system for every VM, and does not restart a running VM. Read [Configuring the VM](#configuring-the-vm) before relying on the interface.

## Example Usage

```terraform
resource "prodata_local_network_attachment" "example" {
  vm_id      = prodata_vm.example.id
  network_id = prodata_local_network.storage.id

  # Optional: without it the platform assigns the next free address of the network.
  ip = "192.168.30.10"
}
```

### Full Example

```terraform
resource "prodata_local_network" "app" {
  name    = "app-network"
  cidr    = "192.168.20.0/24"
  gateway = "192.168.20.1"
}

resource "prodata_local_network" "storage" {
  name    = "storage-network"
  cidr    = "192.168.30.0/24"
  gateway = "192.168.30.1"
}

resource "prodata_vm" "example" {
  name             = "my-vm"
  image_id         = 123
  cpu_cores        = 2
  ram              = 4
  disk_size        = 50
  disk_type        = "SSD"
  local_network_id = prodata_local_network.app.id
  password         = "SecurePassword123"
}

# A second interface in the storage network, with a fixed address.
resource "prodata_local_network_attachment" "storage" {
  vm_id      = prodata_vm.example.id
  network_id = prodata_local_network.storage.id
  ip         = "192.168.30.10"

  # The VM is up when the interface is attached, and the platform does not restart it.
  # Say that you restart it yourself, to silence the warning about it.
  reboot_after_attach = false
}
```

## What the platform allows

- A VM has **one interface per network**. The VM's own network already holds its main interface, so it cannot be attached a second time. Moving the VM to a network it has a second interface in is refused until that attachment is destroyed: see [Moving a VM to another network](vm.md#moving-a-vm-to-another-network).
- The number of interfaces of a VM is limited by the platform; an attach beyond the limit is refused with the limit in the message.
- `ip`, if set, must be an address of the network that is free: not the network address, the broadcast address or the gateway, and not held by another interface of the network. If it is not set, the platform assigns the next free address and it is recorded in `ip`.
- The VM must be `RUNNING` or `STOPPED`. If it is starting, stopping or rebooting, the provider waits for it to settle and repeats the call, within the `create` or `delete` [timeout](#timeouts); a VM in the `ERROR` status ends the wait with the platform's answer. If another operation on the VM is in progress (code 961), the call is repeated at growing intervals for up to about two minutes, and then fails with the platform's answer: apply again once the other operation has ended. If the `create` or `delete` timeout runs out first, the error says that another operation held the VM.
- A VM that is **paused or hibernated** is shown by the platform as `STOPPED`, yet attaching and detaching are refused for it like for a VM in transition (code 959). The provider asks three times in a row and then stops, with a message that says the VM is shown as settled and may be paused or hibernated: resume or stop it, then apply again.
- For these calls the platform answers as if a VM in the `ERROR` or `LOST` status did not exist (code 633). Creating an attachment on such a VM fails with that answer; a refresh and a destroy treat the attachment as gone (see [Drift](#drift) and [Destroying the attachment](#destroying-the-attachment)).
- Virtual machines that the platform manages itself, such as the nodes of a Kubernetes cluster, load balancers and routers, and the members of an auto scaling group, are refused: their interfaces are not changed by hand.
- The platform does not set a default gateway on an added interface. The interface serves the addresses of its own network, which is what a storage or database network needs. Interfaces that carry a gateway are treated as primary, and cannot be detached: see [Destroying the attachment](#destroying-the-attachment). An interface can get a gateway later: when somebody sets one in the console, and, if the VM has a gateway, when somebody edits the address of the interface there.

## Configuring the VM

Whether the new interface and its address work inside the guest operating system depends on the VM:

- **A VM with cloud-init**: the platform adds the interface to the VM and writes its address into the VM's cloud-init settings. Whether the guest applies them without a restart depends on the VM, so on a VM that is *running* when the interface is attached they may take effect only after the VM has been stopped and started again, although the attachment is created and Terraform reports it as done. If `reboot_after_attach` is not set, the apply ends with a warning saying so; see [Restarting the VM](#restarting-the-vm).
- **A VM without cloud-init** is not configured at all: the platform adds the interface to the VM but nothing sets the address up inside the guest. The apply ends with a warning that gives the address and the MAC address of the interface. Configure it inside the VM yourself, for example with netplan or NetworkManager, matching the interface by its MAC address, which is also in `mac_address`. The warning does not depend on `reboot_after_attach`: a restart does not configure the guest.

A VM that is stopped when the interface is attached applies it when it next starts, and the apply produces no warning about a restart.

## Restarting the VM

With `reboot_after_attach = true` the provider restarts the VM as part of creating the attachment. The create does not finish until the platform reports the VM `RUNNING` again, so whatever depends on the attachment is created after the restart:

- A VM that is `RUNNING` when the interface is attached is stopped, and started again once it is `STOPPED`.
- A VM that is stopped is left as it is. It applies the configuration the next time it starts, and the provider does not start a VM that was meant to be off. If the VM was up when the interface was attached and is found stopped when the provider comes to restart it, the apply succeeds with a warning saying so.

The `create` [timeout](#timeouts) bounds the whole create, the attach and the restart together.

~> **Warning:** The platform's stop is not a clean shutdown of the guest operating system: it may power the VM off at once, leaving the guest no chance to flush its buffers or stop its services. If the VM runs something that needs a clean shutdown, such as a database or an NFS server, do not set `reboot_after_attach = true`. Shut the VM down from inside before the attachment is created, or restart it yourself afterwards.

Things to know:

- **Unset is not the same as `false`.** If `reboot_after_attach` is not set and the VM is running when the interface is attached, the apply succeeds with a warning that the interface may work only after a restart. Set the argument to `false` to say that you restart the VM yourself and silence the warning.
- **`RUNNING` is the platform's view of the VM.** The guest operating system may still be booting when `apply` finishes, and the provider cannot check that the address answers. Make whatever needs the new address wait for it.
- **Only when the attachment is created.** Changing `reboot_after_attach` afterwards is an in-place update that is only recorded: it restarts nothing, and neither does importing an attachment. Replacing the attachment creates it again, and restarts the VM again if the argument is `true`.
- **If the restart fails**, the interface already exists on the platform, so the attachment is kept in the state and Terraform marks it as tainted. The next apply detaches the interface and attaches it again. Look at the VM in the panel first: if the failure came after the stop, the VM is stopped, and the provider does not start a VM that is not running, so start it yourself.
- **Destroying the attachment never restarts the VM.**

## Destroying the attachment

Destroying the attachment detaches the interface from the VM, together with the firewall rules and the security group binding that the platform holds for that interface. The VM is not stopped or restarted: restart it yourself if the guest needs to forget the interface. On a VM that does not hot-unplug network devices the destroy fails until the VM has been stopped and started again: see the "platform error" below.

- **Destroy the attachments before the network.** A local network cannot be deleted while a VM has an interface in it. Reference the network in `network_id`, as in the examples, and Terraform destroys the attachment first.
- **A primary interface cannot be detached.** The platform refuses (code 958) an interface that has become the VM's primary one, for example because a default gateway was set on it, or its address was edited, in the console (editing the address of an interface can give it the gateway of the VM, so that the VM holds it on two interfaces). To detach it, move the gateway to the interface that is to carry the VM's default route, and leave this one without it. The console does that in one step: the gateway that is set on an interface is cleared on every other interface of the VM.
  1. Open the VM in the console, then the Gateway settings (in the Russian console: Настройка шлюза) of the interface that is to carry the default route, usually the main one, the one in the VM's own network. The console offers them only for a VM without a public IP: for a VM with one, ask support to move the gateway.
  2. Choose the gateway the VM is to have, an address of the network of that interface, and confirm. Use network gateway (Использовать шлюз сети) gives the VM the gateway of that network, which is the one most VMs have. Use another IP (Использовать другой IP) takes any address of that network: choose it and enter that address if the VM was created with a gateway of its own, which Use network gateway would replace. Choose it also if that interface holds the right gateway already, as it can after an edit of an address: the console does not let you confirm Use network gateway where it is in effect already, but it accepts the same address entered as another IP, and clears the gateway on the other interfaces. If another interface holds a gateway, the console says so before you confirm, and that the gateway will be disabled.

  Do not choose No gateway (Без шлюза) unless the VM is to have no default gateway at all: it clears the gateway of every interface of the VM, and the VM has none until one is set again.

  The interface is not primary any more, and the destroy detaches it. If the destroy still answers 958, open the Gateway settings of the interface in the console again: if they show a gateway on it, the change did not go through, so repeat it; if they show none, ask support.
- **An interface that is already gone is not an error.** If the platform answers that the interface (code 703) or the VM (code 633) does not exist, the destroy succeeds. It answers 633 also for a VM in the `ERROR` or `LOST` status. A plain HTTP 404 without the platform's answer, such as the error page of a proxy, is not taken for that: it is reported as an error and the attachment stays in the state.
- **A "platform error" (627) has three usual causes. Try them in this order.**
  1. *A passing failure of the platform or of Proxmox.* Run `terraform destroy` again. Repeating it is harmless, and it costs the VM no downtime.
  2. *The removal is queued.* A VM whose `hotplug` setting does not include `network`, or whose guest did not release the device, does not drop the interface at once: Proxmox queues the change, and it takes effect only when the VM has been stopped and started again. A reboot from inside the guest is not enough. Stop the VM (shut the guest down from inside for a clean stop), start it and run `terraform destroy` again: it finds the interface gone and finishes.
  3. *The interface was waiting for a restart to appear.* It was attached, without `reboot_after_attach`, to a VM that does not hot-plug network devices, and the VM had not been restarted since. The stop and start of the previous step then brought it up, and its removal is queued in turn: if the destroy fails again, stop and start the VM once more and run `terraform destroy` again. If that does not describe the VM, skip this one.

  Do not remove the attachment from the state (`terraform state rm`) in the meantime: a change that is still queued takes effect when the VM next starts, and an interface that was only waiting to appear would then appear without Terraform knowing of it.
- **If the destroy still fails after that, contact support and do not detach in the console.** The platform may have found another interface than this one at the interface's index, for example after its tag or bridge was changed by hand. The console detaches by that index, so it may detach another interface of the VM, the public one included. Ask support to check the VM's configuration first.

## Drift

The attachment is read back on every refresh. It is removed from the state, and so planned for creation again, when the platform answers that the VM (code 633) or the interface (code 703) does not exist, which it does also for a VM in the `ERROR` or `LOST` status, or when the interface now belongs to another network. Any other error from the platform, such as a timeout or a plain HTTP 404 page without the platform's answer, is reported and leaves the state as it is.

If the address of the interface is changed outside Terraform, for example in the console, the refresh ends with a warning that names the address in the panel. The state and the plan are left as they are: Terraform does not detach and attach an interface because somebody changed its address on purpose. There are two ways out:

- **Keep the address of the panel.** Set `ip` to it in the configuration, then take the interface over again: `terraform state rm prodata_local_network_attachment.<name>`, then `terraform import prodata_local_network_attachment.<name> <vm_id>:<interface_id>`. Setting `ip` alone is no way out: a changed `ip` requires replacement, so the plan would detach the interface and attach a new one, with another MAC address.
- **Get the configured address back.** Replace the attachment: `terraform apply -replace=prodata_local_network_attachment.<name>`. This detaches the interface and attaches a new one. It is the only way when the platform does not report an address for the interface.

An interface whose address was edited in the console can also have become the VM's primary interface, because the console can give an edited interface the gateway of the VM. Neither way out works then: the platform does not detach a primary interface, so the replacement ends with the error 958, and the import refuses one. The warning says so. Move the gateway to the interface that is to carry the VM's default route first, as described in [Destroying the attachment](#destroying-the-attachment), and take either way after that. If the warning says that the panel lists no gateway on the interface, moving a gateway changes nothing: ask support why the platform treats the interface as primary.

## If an apply is cut short

An apply can end after the attach request has gone out and before Terraform has recorded the interface: it is interrupted, the `create` [timeout](#timeouts) runs out, or the connection is lost. The platform may have created the interface all the same, and then Terraform does not know of it. The provider does not repeat an attach that got no answer, and the platform would not take a repeat for a second interface: a VM has one interface per network, so the repeat is refused. The provider looks at the interfaces of the VM instead, and the error says what it found:

- If the attach got no answer (a gateway timeout, a dropped connection, a timeout), the message says so and names the interface that the VM now has in that network, if there is one.
- If the attach is refused because the VM already has an interface in that network (code 955), in a later apply or on the repeat of a request that a gateway answered with 503, the message names that interface in the same way.
- If the only interface the VM has in that network is a primary one, the network is either the VM's own, or one where the interface carries a default gateway: the platform treats both as primary, and the list does not say which. The resource does not manage a primary interface and refuses to import it. When the interface carries no gateway, the network is the VM's own: the message says so and offers no import, and the way out is to choose another network. When it carries one, the message names both possibilities. If the network is the VM's own, choose another network. If it is not, and the interface does not need to carry the gateway, move the gateway to the interface that is to carry the VM's default route, as described in [Destroying the attachment](#destroying-the-attachment) (the console clears the gateway on every other interface of the VM when one is set on an interface), and then import the interface with the command in the message.

To bring the interface under Terraform, run the command from the message, `terraform import prodata_local_network_attachment.<name> <vm_id>:<interface_id>`, and apply again. If no interface is listed yet, the platform may still be finishing the request: before applying again, look at the interfaces of the VM and their ids with `GET /api/v2/vms/<vm_id>/local-networks` of the ProData API. An interface that was not created by Terraform, for example in the console, is imported in the same way if Terraform is to manage it from then on; otherwise choose another network.

## Schema

### Required

- `vm_id` (Number) The ID of the virtual machine to add the interface to. Changing this forces a new resource.
- `network_id` (Number) The ID of the local network to attach the VM to. A VM can have only one interface in a network, and cannot be attached to its own network, the one of its main interface, a second time. Changing this forces a new resource.

### Optional

- `ip` (String) The IPv4 address of the interface in the network. If not specified, the platform assigns the next free address of the network, and it is recorded here. Changing this forces a new resource. An address that is changed outside Terraform is reported as a warning when the state is refreshed and does not change the plan.
- `region` (String) Region ID override. If not specified, uses the provider's default region. Changing this forces a new resource.
- `project_tag` (String) Project tag override. If not specified, uses the provider's default project_tag. Changing this forces a new resource.
- `reboot_after_attach` (Boolean) Restart the VM during `apply`, once the interface is attached, if the VM is running. See [Restarting the VM](#restarting-the-vm). The restart is the platform's stop followed by its start, and that stop is not a clean shutdown of the guest operating system: it may power the VM off at once. A VM that is not running is left alone. Only used when the attachment is created: changing it afterwards does nothing, and the VM is never restarted when the attachment is destroyed. If unset, the VM is not restarted and a running VM produces a warning; set it to `false` to say that you restart the VM yourself and silence the warning.
- `timeouts` (Object) See [Timeouts](#timeouts) below.

### Attribute Reference

- `interface_id` (Number) The ID of the interface (not of the network). It identifies the interface for detaching and for import.
- `mac_address` (String) The MAC address of the interface. Not set when the platform does not know it.

### Timeouts

```terraform
resource "prodata_local_network_attachment" "example" {
  # ...

  timeouts = {
    create = "30m"
    delete = "20m"
  }
}
```

- `create` (String) Default `15m`. A ceiling, not a fixed wait: the provider returns as soon as the attach call has returned and, with `reboot_after_attach`, the VM is `RUNNING` again.
- `delete` (String) Default `10m`. The longest a detach waits for a VM that is neither `RUNNING` nor `STOPPED` to become one of them. The provider returns as soon as the interface is detached.

## Import

Local network attachments can be imported using `vm_id:interface_id`:

```shell
terraform import prodata_local_network_attachment.example <vm_id>:<interface_id>
```

Example:

```shell
terraform import prodata_local_network_attachment.example 123:456
```

`interface_id` is the ID of the interface, not of the network. It is the `id` of the interface in the platform's API for the VM's local networks, and the `interface_id` of an attachment created by Terraform.

Import with the provider of the VM's region and project. The VM is looked up in the region and project the provider is configured for (or the `region` and `project_tag` of the provider block you import with), and a VM in another project is not found.

The primary interface of a VM, the one in the VM's own network, or one with a default gateway, cannot be imported: it cannot be detached either, so this resource does not manage it. The main interface belongs to the `prodata_vm`, and its network is changed there: see [Moving a VM to another network](vm.md#moving-a-vm-to-another-network). An interface that is primary only because it carries the gateway of the VM can be imported once the gateway has been moved to another interface: see [Destroying the attachment](#destroying-the-attachment).

`network_id` and `ip` are read from the platform after the import. `reboot_after_attach` and `timeouts` are not read from the platform. After an import, the first apply with either set in the configuration records it in the state and changes nothing else.
