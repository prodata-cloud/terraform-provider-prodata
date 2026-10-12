---
page_title: "prodata_public_ip_attachment Resource - ProData Provider"
subcategory: "Networking"
description: |-
  Attaches a ProData public IP to a virtual machine.
---

# prodata_public_ip_attachment (Resource)

Attaches a ProData public IP to a virtual machine. Destroying this resource detaches the public IP from the VM.

~> **Note:** `vm_id`, `public_ip_id`, `region` and `project_tag` require resource replacement when changed. Any change will detach and reattach the public IP. `reboot_after_attach` and `timeouts` can be changed in place, but only a new attachment makes use of them.

~> **Note:** The platform applies the network configuration of a public IP only when the VM starts. On a VM that is running, the address does not work until the VM has been stopped and started again, although the attachment is created and Terraform reports it as done. Set `reboot_after_attach = true` to have the provider restart the VM during `apply`, or restart the VM yourself. See [Restarting the VM](#restarting-the-vm).

## Example Usage

```terraform
resource "prodata_public_ip_attachment" "example" {
  vm_id        = prodata_vm.example.id
  public_ip_id = prodata_public_ip.example.id
}
```

### Full Example

```terraform
resource "prodata_local_network" "example" {
  name    = "my-network"
  cidr    = "10.0.0.0/24"
  gateway = "10.0.0.1"
}

resource "prodata_public_ip" "example" {
  name = "my-public-ip"
}

resource "prodata_vm" "example" {
  name             = "my-vm"
  image_id         = 123
  cpu_cores        = 2
  ram              = 4
  disk_size        = 50
  disk_type        = "SSD"
  local_network_id = prodata_local_network.example.id
  password         = "SecurePassword123"
}

resource "prodata_public_ip_attachment" "example" {
  vm_id        = prodata_vm.example.id
  public_ip_id = prodata_public_ip.example.id

  # Power-cycle the VM when the address is attached, so that the address works when apply
  # finishes. The platform's stop is not a clean shutdown, and it happens again whenever
  # this attachment is replaced: set it only for a VM that can take that.
  reboot_after_attach = true
}
```

## Restarting the VM

Attaching a public IP changes the VM's network configuration, and the platform applies the change only when the VM starts. A VM that is running keeps its old configuration until it is stopped and started again. `terraform apply` cannot tell the difference: the attachment exists, so it is recorded as created, and anything that depends on it goes on.

With `reboot_after_attach = true` the provider restarts the VM as part of creating the attachment. The create does not finish until the platform reports the VM `RUNNING` again, so whatever depends on the attachment is created after the restart:

- A VM that is `RUNNING` is stopped, and started again once it is `STOPPED`.
- A VM that is starting or rebooting is waited for first, because it may have read its configuration before the public IP was attached. It is then restarted.
- A VM that is stopped, or on its way to being stopped, is left as it is. It applies the configuration the next time it starts, and the provider does not start a VM that was meant to be off. If the VM was up when the public IP was attached and is found stopped when the provider comes to restart it, the apply succeeds with a warning saying so.

The `create` [timeout](#timeouts) bounds the whole create, the attach and the restart together.

~> **Warning:** The platform's stop is not a clean shutdown of the guest operating system: it may power the VM off at once, leaving the guest no chance to flush its buffers or stop its services. If the VM runs something that needs a clean shutdown, such as a database or an NFS server, do not set `reboot_after_attach = true`. Shut the VM down from inside before the attachment is created, or restart it yourself afterwards.

Things to know:

- **Unset is not the same as `false`.** If `reboot_after_attach` is not set and the VM is up when the public IP is attached, the apply succeeds with a warning that the address works only after a restart. Set the argument to `false` to say that you restart the VM yourself and silence the warning.
- **`RUNNING` is the platform's view of the VM.** The guest operating system may still be booting when `apply` finishes, and the provider cannot check that the address answers. Make whatever needs the new address wait for it.
- **Only when the attachment is created.** Changing `reboot_after_attach` afterwards is an in-place update that is only recorded: it restarts nothing, and neither does importing an attachment. Replacing the attachment (changing `vm_id`, `public_ip_id`, `region` or `project_tag`) creates it again, and restarts the VM again if the argument is `true`.
- **A call that fails is judged by the VM.** The platform sometimes answers a stop or a start with an error although it carried the call out: its generic error, a gateway that times out or restarts, a dropped connection. The provider does not take such an error at its word. It looks at the VM's status and goes on from what it finds, and it repeats a call that did not go through for about three minutes before it gives up, because the platform's own view of a VM can trail the hypervisor's by more than a minute. Reading the VM's status gets the same patience once the provider has stopped the VM, and so does a VM that reads `STOPPED` again after the platform accepted its start. The `create` [timeout](#timeouts) still bounds all of it, so a call that hangs for a long time may end the restart sooner. An answer that is the platform's last word, such as "forbidden", ends the restart at once.
- **If the restart fails**, the attachment already exists on the platform, so it is kept in the state and Terraform marks it as tainted. The next apply detaches the public IP and attaches it again. Look at the VM in the panel first: if the failure came after the stop, the VM is stopped, and the next apply leaves a stopped VM stopped as well, so start it yourself.
- **Destroying the attachment never restarts the VM.** The platform takes the local network's default gateway off the VM when a public IP is attached, and does not currently put it back when the public IP is detached. A VM that has applied the attachment can therefore be left without a default route after the detach, at once or at its next restart, and restarting it would not bring the gateway back. After destroying the attachment, check the VM's routing and restore the gateway if it is missing.

## Schema

### Required

- `vm_id` (Number) The ID of the virtual machine to attach the public IP to. Changing this forces a new resource.
- `public_ip_id` (Number) The ID of the public IP to attach. Changing this forces a new resource.

### Optional

- `region` (String) Region ID override. If not specified, uses the provider's default region. Changing this forces a new resource.
- `project_tag` (String) Project tag override. If not specified, uses the provider's default project_tag. Changing this forces a new resource.
- `reboot_after_attach` (Boolean) Restart the VM during `apply`, once the public IP is attached, so that the address works when `apply` finishes. See [Restarting the VM](#restarting-the-vm). The restart is the platform's stop followed by its start, and that stop is not a clean shutdown of the guest operating system: it may power the VM off at once. A VM that is not running is left alone. Only used when the attachment is created: changing it afterwards does nothing, and the VM is never restarted when the attachment is destroyed. If unset, the VM is not restarted and a VM that is running produces a warning; set it to `false` to say that you restart the VM yourself and silence the warning.
- `timeouts` (Object) See [Timeouts](#timeouts) below.

### Attribute Reference

- `public_ip` (String) The public IP address string assigned to the VM.

### Timeouts

```terraform
resource "prodata_public_ip_attachment" "example" {
  # ...

  timeouts = {
    create = "30m"
  }
}
```

- `create` (String) Default `15m`. A ceiling, not a fixed wait: the provider returns as soon as the attach call has returned and, with `reboot_after_attach`, the VM is `RUNNING` again.

## Import

Public IP attachments can be imported using `vm_id:public_ip_id`:

```shell
terraform import prodata_public_ip_attachment.example <vm_id>:<public_ip_id>
```

Example:

```shell
terraform import prodata_public_ip_attachment.example 123:456
```

`reboot_after_attach` and `timeouts` are not read from the platform. After an import, the first apply with either set in the configuration records it in the state and changes nothing else.
