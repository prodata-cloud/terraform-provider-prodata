---
page_title: "prodata_vm Resource - ProData Provider"
subcategory: "Compute"
description: |-
  Manages a ProData virtual machine.
---

# prodata_vm (Resource)

Manages a ProData virtual machine.

~> **Note:** Most attributes require resource replacement when changed (destroy and recreate). The `name`, `cpu_cores`, `ram`, `disk_size`, `disk_type`, and `dns` attributes can be updated in-place — changing them forces a VM reboot but not a full replacement. So does `local_network_id`: the VM is moved to another network without being recreated, which stops and starts it — see [Moving a VM to another network](#moving-a-vm-to-another-network).

~> **Note:** The platform applies the network configuration of a public IP only when the VM starts, so a public IP attached to a running VM (via `prodata_public_ip_attachment`) becomes active only after the VM has been stopped and started again. Set `reboot_after_attach = true` on the attachment to have the provider do that during `apply`.

~> **Note:** After a delete request, the provider checks that the VM is absent from the panel. If the panel still reports it, or its absence cannot be confirmed, Terraform keeps the VM in state and reports an error. The platform may finish hypervisor deletion asynchronously.

## Example Usage

```terraform
resource "prodata_vm" "example" {
  name             = "my-vm"
  image_id         = 123
  cpu_cores        = 2
  ram              = 4
  disk_size        = 50
  disk_type        = "SSD"
  local_network_id = 456
  private_ip       = "10.0.0.10"
  password         = "SecurePassword123!"
}
```

### With Public IP and SSH Key

```terraform
resource "prodata_vm" "web_server" {
  name             = "web-server"
  image_id         = 123
  cpu_cores        = 4
  ram              = 8
  disk_size        = 100
  disk_type        = "NVME"
  local_network_id = 456
  private_ip       = "10.0.0.20"
  public_ip_id     = 789
  password         = "SecurePassword123!"
  ssh_public_key   = "ssh-rsa AAAAB3NzaC1yc2E..."
  description      = "Production web server"
}
```

### With cloud-init user data

`user_data` is **write-only**: the raw payload is never stored in Terraform state nor
shown in a plan (this requires Terraform >= 1.11). Because Terraform cannot diff a
write-only value, the provider computes a `sha256` of the payload and keeps it in the
resource's private state; when the payload changes it **replaces** the VM so cloud-init
re-runs at first boot. You do not set or manage a hash yourself.

```terraform
terraform {
  required_version = ">= 1.11" # user_data is a write-only argument
}

locals {
  user_data = <<-EOT
    #cloud-config
    package_update: true
    packages:
      - htop
    runcmd:
      - [ systemctl, enable, --now, qemu-guest-agent ]
  EOT
}

resource "prodata_vm" "bootstrapped" {
  name             = "bootstrapped"
  image_id         = 123
  cpu_cores        = 2
  ram              = 4
  disk_size        = 50
  disk_type        = "SSD"
  local_network_id = 456
  password         = "SecurePassword123!"

  user_data = local.user_data

  timeouts = {
    # 30m covers the in-guest cloud-init run, including slower Windows guests.
    # Linux guests are faster and return as soon as the VM is reported ready.
    create = "30m"
  }
}
```

~> **Note:** VM creation is asynchronous: the create call returns before the VM is ready,
and Terraform waits by polling until the VM is reported ready — which includes the in-guest
cloud-init run. The provider validates only the `user_data` prefix and the 64 KiB size limit
client-side; the cloud-config structure is validated by the backend. A cloud-init failure
inside the guest is **not** reported back — a VM whose cloud-init failed still reports
`RUNNING`. A successful `apply` therefore does not by itself prove the `user_data` script ran
without errors; verify on the guest if that matters.

## Changing DNS

Changing `dns` on a running VM stops and starts it. Depending on the image and cloud-init
configuration, the changed data may be treated as a new instance: SSH host keys can be regenerated
and once-per-instance scripts can run again. A VM without cloud-init needs DNS configured in the
guest separately. If the restart cannot be confirmed, apply returns an error and records
`restart_pending`; the next apply attempts to restore the VM.
Deletion or replacement is blocked until that restart has been recovered. If the resource was
removed from configuration or a replacement was requested, restore its previous configuration and
apply to recover it before deleting or replacing it.

If a VM was created but DNS confirmation failed, `dns_confirmation_pending` protects it from
deletion during Terraform's automatic replacement of a tainted resource. Verify the VM and DNS,
run `terraform untaint` for that resource, and apply again to confirm or reconcile DNS before
attempting deletion or replacement.

## Moving a VM to another network

Changing `local_network_id` moves the main network interface of the VM to the other network. The VM is **not** recreated: it keeps its `id`, its disks, its public IP and its other interfaces. (Earlier versions of the provider replaced the VM on this change. To get that replacement now, run `terraform apply -replace=prodata_vm.<name>`; it deletes the disks.)

~> **Note:** The guest operating system has to learn its new address. A VM **with cloud-init** gets the new network settings from the platform, and the guest applies them when the VM starts; depending on the image, cloud-init may take the changed settings for a new instance, regenerate the SSH host keys and run `user_data` again. A VM **without cloud-init** is configured by nothing: after the move it has no network until you set the new address, the mask of the new network and its gateway inside the VM yourself. The plan warns about it, and so does the apply, naming the address, the network whose mask applies and the gateway.

```terraform
resource "prodata_local_network" "legacy" {
  name    = "legacy"
  cidr    = "192.168.20.0/24"
  gateway = "192.168.20.1"
}

resource "prodata_local_network" "app" {
  name    = "app"
  cidr    = "192.168.30.0/24"
  gateway = "192.168.30.1"
}

resource "prodata_vm" "example" {
  name      = "my-vm"
  image_id  = 123
  cpu_cores = 2
  ram       = 4
  disk_size = 50
  disk_type = "SSD"
  password  = "SecurePassword123!"

  # Was prodata_local_network.legacy.id. Changing it moves the VM; it does not recreate it.
  local_network_id = prodata_local_network.app.id

  # An address of the new network. Leave it out to get the next free one.
  private_ip = "192.168.30.10"
}
```

What a move does:

- **Downtime.** A running VM is stopped for the move and started again afterwards; a stopped VM stays stopped. The stop is a power-off and the guest is not shut down: shut down from inside, beforehand, whatever needs a clean stop, such as a database or an NFS server.
- **A new address.** The VM gets an address in the new network: `private_ip` if it is set, otherwise the next free one (the plan shows it as `known after apply`). **The old address is released at once** and may be given to another VM of the old network, so update whatever refers to it: DNS records, allow-lists, NFS exports. The name of the interface inside the guest may change as well.
- **What moves with the interface.** Its default gateway and its security group. The public IP of the VM is not touched.
- **What is refused.** The platform checks the move before anything is changed, and refuses it for a VM that is a backend of a load balancer (remove it from the load balancer first); is a member of an auto scaling group, or has a role the platform manages itself, such as a router or a node of a Kubernetes cluster; already has an interface in the new network (destroy its `prodata_local_network_attachment` first); carries a default gateway while the new network has none (set a `gateway` on the network, or choose another one); or has more than one security group on the interface, or a group without rules (leave it one group that has rules).
- **Order.** `terraform apply` asks the platform to check the move first; if it refuses, nothing is changed, not even the name of the VM. Then it renames the VM, stops it, resizes it (`cpu_cores`, `ram`, `disk_size`, `disk_type`), moves the interface and starts the VM.

Things to know:

- **Set `private_ip` together with `local_network_id`, or not at all.** A configuration that pins `private_ip` has to change it in the same apply to an address of the new network: the old address is not in the new network, and the check refuses it. Changing `private_ip` alone, without `local_network_id`, still replaces the VM.
- **A failure is repeated by applying again.** The error names the step that failed, and the provider starts the VM again if it stopped it (the error says whether that worked). An apply that is interrupted, or that loses its connection, is repaired in the same way: the platform finishes a move that was cut short, or takes it back, when it is asked again. The exception is the error "An earlier move of the virtual machine needs the operator": the platform can neither finish nor take back that move by itself, repeating the apply does not help, and you need to contact support.
- **The whole update has to fit in `timeouts.update`.** The default is `30m`, and a plan that moves the VM is refused when the timeout is shorter than `20m`. See [Timeouts](#timeouts).
- **Do not change the interfaces or the public IP of the same VM in the same apply.** While a move is open the platform refuses to attach or detach an interface or a public IP of the VM, because it is busy. Terraform orders resources that refer to `prodata_vm.<name>` after the VM; if a `prodata_local_network_attachment` or a `prodata_public_ip_attachment` does not refer to it, add `depends_on = [prodata_vm.<name>]`. An attachment that is already in the new network blocks the move: destroy it in an earlier apply.
- **Exchanging networks or addresses between two VMs in one apply** can fail for one of them with "address taken", while the other has not left the address yet. Apply again.
- **`lifecycle { create_before_destroy = true }` changes nothing here:** a move is an update, not a replacement.
- **A network created in the same apply** works too. The plan then warns that the new network is known only after other resources are applied, and that the VM is moved only if it turns out to be another network than the one it is in. Pin `private_ip` there only when the VM is to move: if the network turns out to be the one the VM is in already and `private_ip` is a different address, Terraform stops with "Provider produced inconsistent final plan" before anything is changed, and applying again replaces the VM, as a change of `private_ip` alone does.
- **A `prodata_local_network` that has VMs in it cannot be replaced.** A change of its `cidr` or `gateway` destroys the network before the VMs that refer to it are updated, and the platform refuses to delete a network that VMs are in: the apply stops with the platform's error and nothing is moved. (Earlier versions of the provider replaced the VMs first, with their disks.) Move the VMs to another network in an apply of their own, then change the network.
- **An older panel** that does not have the call refuses the move with "The panel cannot move a virtual machine to another network" before anything is changed. Update the panel, or replace the VM with `terraform apply -replace=prodata_vm.<name>`.

## Schema

### Required

- `name` (String) The name of the virtual machine. Must be 3-63 characters, contain at least one letter, only letters, numbers, and hyphens. Can be updated in-place.
- `image_id` (Number) The ID of the image to use for the virtual machine. Changing this forces a new resource.
- `cpu_cores` (Number) The number of CPU cores for the virtual machine. Minimum 1. Changing this forces a VM reboot.
- `ram` (Number) The amount of RAM in GB for the virtual machine. Minimum 1. Changing this forces a VM reboot.
- `disk_size` (Number) The size of the disk in GB. Minimum 10. Can only be increased. Changing this forces a VM reboot.
- `disk_type` (String) The type of disk (HDD, SSD, or NVME). Can only be upgraded (e.g. HDD → SSD). Changing this forces a VM reboot.
- `local_network_id` (Number) The ID of the local network of the VM's main network interface. Changing it moves the interface to another network in place: the VM is not recreated, a running VM is stopped (powered off) and started again, and the VM gets a new address (see `private_ip`). See [Moving a VM to another network](#moving-a-vm-to-another-network).
- `password` (String, Sensitive) The password for the virtual machine. Changing this forces a new resource.

### Optional

- `region` (String) Region where the VM will be created. If not specified, uses the provider's default region. Changing this forces a new resource.
- `project_tag` (String) Project tag where the VM will be created. If not specified, uses the provider's default project_tag. Changing this forces a new resource.
- `private_ip` (String) The private IP address of the virtual machine in its local network. If not specified, an available IP is auto-assigned from the local network. Changing it together with `local_network_id` sets the address in the new network; changing it alone forces a new resource.
- `public_ip_id` (Number) The ID of a public IP to attach to the VM at creation time. If not specified, no public IP is attached. Changing this forces a new resource.
- `ssh_public_key` (String) SSH public key for authentication. Changing this forces a new resource.
- `description` (String) Description of the virtual machine. Changing this forces a new resource.
- `dns` (String) One to four IPv4 nameservers separated by single spaces, for example `"1.1.1.1 8.8.8.8"`. If omitted at creation, the platform chooses its default and Terraform does not manage DNS. Removing the attribute stops managing DNS without clearing the configured value. A change to DNS stops and starts a running VM; the stop may immediately power it off. DNS is a cloud-init configuration and does not prove the guest applied it. A guest without cloud-init needs its own DNS configuration. A panel with the VM network-settings API is required.
- `user_data` (String, Write-only) Cloud-init user data applied at first boot via a NoCloud ISO. Must begin with `#cloud-config` or a shebang (`#!`) and not exceed 64 KiB (65536 bytes). Write-only: never stored in state nor shown in a plan (requires Terraform >= 1.11). The provider hashes the payload (sha256) and forces a new resource when it changes, to re-run cloud-init.
- `timeouts` (Object) See [Timeouts](#timeouts) below.

### Attribute Reference

- `id` (Number) The unique identifier of the virtual machine.
- `guid` (String) The VM's globally-unique identifier assigned by the panel. Use this to reference the VM as a load balancer backend (`prodata_lb.backend_group.vm_ids`).
- `status` (String) The current status of the virtual machine (CREATING, RUNNING, STOPPED, etc.). A plan that stops and starts the VM (a change of `cpu_cores`, `ram`, `disk_size`, `disk_type`, `dns` or `local_network_id`) shows it as `known after apply`.
- `public_ip` (String) The public IP address assigned to the virtual machine (if any).
- `image_name` (String) The name of the OS image (e.g. `Ubuntu 22.04`). Populated from the API.
- `image_slug` (String) The slug of the OS template (e.g. `ubuntu-22.04`). Null for custom images and for VMs created before this feature.
- `restart_pending` (Boolean) A network update stopped a running VM but its restart was not confirmed.
- `dns_confirmation_pending` (Boolean) DNS requested at creation has not yet been confirmed; VM deletion is blocked until recovery.

### Timeouts

```terraform
resource "prodata_vm" "example" {
  # ...

  timeouts = {
    create = "30m"
    update = "30m"
  }
}
```

- `create` (String) Default `30m`. Time to wait for the VM (including the in-guest cloud-init run) to become ready. A ceiling, not a fixed wait: the provider returns as soon as the VM is reported ready.
- `update` (String) Default `30m`. The ceiling of the whole update: the rename, the stop, the resize, the DNS change, the move to another network and the start. A ceiling, not a fixed wait: the provider returns as soon as the work is done. A plan that moves the VM to another network is refused when `update` is shorter than `20m`. A VM that the update stopped, or may have stopped because the platform's answer to the stop did not say, is started again however the update ended, even when the ceiling has run out: that can take a few minutes beyond it. When the update did its work, the provider waits for the VM to be running; when a step failed, the start is handed to the platform, and the VM may still be starting when the error is reported. If a network-settings request has an unresolved outcome, the platform blocks start until support has checked it; the VM may remain stopped. A stop that the platform refuses for good leaves the VM as it was. A VM that still reads as running after a stop whose outcome is not known is looked at for a few minutes before it is taken to be up, because the platform's view of a VM can lag behind. A start that fails in a way that may pass is tried again for a few minutes.

## Import

VMs can be imported using their ID:

```shell
terraform import prodata_vm.example <vm_id>
```

Example:

```shell
terraform import prodata_vm.example 123
```

~> **Note:** The `password` and `ssh_public_key` attributes are write-only and cannot be read back from the API. After import, these attributes will be empty in state. If your configuration specifies them, Terraform will show a diff but no replacement will be forced.

~> **Note:** `user_data` is write-only and is empty in state after import. Because the change-detection hash lives in private state (seeded only when the VM is created by Terraform), an **imported** VM is not tracked for `user_data` changes until it is next replaced — editing `user_data` on an imported VM will not, on its own, trigger a replacement.

### Deletion confirmation

On compatible panels, VM deletion waits for the hypervisor task to finish. The provider polls the
panel's explicit deletion-pending response for up to 10 minutes. Cancellation, a network failure,
or an unconfirmed result retains Terraform state. Resolve the outstanding operation before retrying;
an arbitrary error is not automatically retried as a safe deletion.
