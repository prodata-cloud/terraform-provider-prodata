---
page_title: "prodata_vm Resource - ProData Provider"
subcategory: "Compute"
description: |-
  Manages a ProData virtual machine.
---

# prodata_vm (Resource)

Manages a ProData virtual machine.

~> **Note:** Most attributes require resource replacement when changed (destroy and recreate). The `name`, `cpu_cores`, `ram`, `disk_size`, and `disk_type` attributes can be updated in-place — changing them forces a VM reboot but not a full replacement.

~> **Note:** After attaching a public IP to a VM (via `prodata_public_ip_attachment`), the VM must be rebooted for the IP to become active.

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

  timeouts {
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

## Schema

### Required

- `name` (String) The name of the virtual machine. Must be 3-63 characters, contain at least one letter, only letters, numbers, and hyphens. Can be updated in-place.
- `image_id` (Number) The ID of the image to use for the virtual machine. Changing this forces a new resource.
- `cpu_cores` (Number) The number of CPU cores for the virtual machine. Minimum 1. Changing this forces a VM reboot.
- `ram` (Number) The amount of RAM in GB for the virtual machine. Minimum 1. Changing this forces a VM reboot.
- `disk_size` (Number) The size of the disk in GB. Minimum 10. Can only be increased. Changing this forces a VM reboot.
- `disk_type` (String) The type of disk (HDD, SSD, or NVME). Can only be upgraded (e.g. HDD → SSD). Changing this forces a VM reboot.
- `local_network_id` (Number) The ID of the local network to attach the VM to. Changing this forces a new resource.
- `password` (String, Sensitive) The password for the virtual machine. Changing this forces a new resource.

### Optional

- `region` (String) Region where the VM will be created. If not specified, uses the provider's default region. Changing this forces a new resource.
- `project_tag` (String) Project tag where the VM will be created. If not specified, uses the provider's default project_tag. Changing this forces a new resource.
- `private_ip` (String) The private IP address for the virtual machine. If not specified, an available IP will be auto-assigned from the local network. Changing this forces a new resource.
- `public_ip_id` (Number) The ID of a public IP to attach to the VM at creation time. If not specified, no public IP is attached. Changing this forces a new resource.
- `ssh_public_key` (String) SSH public key for authentication. Changing this forces a new resource.
- `description` (String) Description of the virtual machine. Changing this forces a new resource.
- `user_data` (String, Write-only) Cloud-init user data applied at first boot via a NoCloud ISO. Must begin with `#cloud-config` or a shebang (`#!`) and not exceed 64 KiB (65536 bytes). Write-only: never stored in state nor shown in a plan (requires Terraform >= 1.11). The provider hashes the payload (sha256) and forces a new resource when it changes, to re-run cloud-init.
- `timeouts` (Block, Optional) Configurable operation timeouts.
  - `create` (String) Time to wait for the VM (including the in-guest cloud-init run) to become ready. Defaults to `30m`.

### Attribute Reference

- `id` (Number) The unique identifier of the virtual machine.
- `guid` (String) The VM's globally-unique identifier assigned by the panel. Use this to reference the VM as a load balancer backend (`prodata_lb.backend_group.vm_ids`).
- `status` (String) The current status of the virtual machine (CREATING, RUNNING, STOPPED, etc.).
- `public_ip` (String) The public IP address assigned to the virtual machine (if any).
- `image_name` (String) The name of the OS image (e.g. `Ubuntu 22.04`). Populated from the API.
- `image_slug` (String) The slug of the OS template (e.g. `ubuntu-22.04`). Null for custom images and for VMs created before this feature.

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
