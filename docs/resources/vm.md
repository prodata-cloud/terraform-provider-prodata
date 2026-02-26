---
page_title: "prodata_vm Resource - ProData Provider"
description: |-
  Manages a ProData virtual machine.
---

# prodata_vm (Resource)

Manages a ProData virtual machine.

~> **Note:** Most attributes require resource replacement when changed (destroy and recreate). The `name` attribute can be updated in-place without recreating the VM.

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

## Schema

### Required

- `name` (String) The name of the virtual machine. Must be 3-63 characters, contain at least one letter, only letters, numbers, and hyphens. Can be updated in-place.
- `image_id` (Number) The ID of the image to use for the virtual machine. Changing this forces a new resource.
- `cpu_cores` (Number) The number of CPU cores for the virtual machine. Minimum 1. Changing this forces a new resource.
- `ram` (Number) The amount of RAM in GB for the virtual machine. Minimum 1. Changing this forces a new resource.
- `disk_size` (Number) The size of the disk in GB. Minimum 10. Changing this forces a new resource.
- `disk_type` (String) The type of disk (HDD, SSD, or NVME). Changing this forces a new resource.
- `local_network_id` (Number) The ID of the local network to attach the VM to. Changing this forces a new resource.
- `password` (String, Sensitive) The password for the virtual machine. Changing this forces a new resource.

### Optional

- `region` (String) Region where the VM will be created. If not specified, uses the provider's default region. Changing this forces a new resource.
- `project_tag` (String) Project tag where the VM will be created. If not specified, uses the provider's default project_tag. Changing this forces a new resource.
- `private_ip` (String) The private IP address for the virtual machine. If not specified, an available IP will be auto-assigned from the local network. Changing this forces a new resource.
- `public_ip_id` (Number) The ID of the public IP to attach to the VM. Changing this forces a new resource.
- `ssh_public_key` (String) SSH public key for authentication. Changing this forces a new resource.
- `description` (String) Description of the virtual machine. Changing this forces a new resource.

### Attribute Reference

- `id` (Number) The unique identifier of the virtual machine.
- `status` (String) The current status of the virtual machine (CREATING, RUNNING, STOPPED, etc.).
- `public_ip` (String) The public IP address assigned to the virtual machine (if any).

## Import

VMs cannot be imported as the API does not provide sufficient information to reconstruct the Terraform state.
