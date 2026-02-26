---
page_title: "prodata_volume_attachment Resource - ProData Provider"
description: |-
  Attaches a ProData volume to a virtual machine.
---

# prodata_volume_attachment (Resource)

Attaches a ProData volume to a virtual machine. Destroying this resource detaches the volume from the VM.

~> **Note:** All attributes require resource replacement when changed. Any change will detach and reattach the volume.

~> **Note:** If the VM is running when this resource is destroyed, the provider will automatically stop the VM, detach the volume, and restart the VM.

## Example Usage

```terraform
resource "prodata_volume_attachment" "example" {
  vm_id     = prodata_vm.example.id
  volume_id = prodata_volume.example.id
}
```

### Full Example

```terraform
resource "prodata_local_network" "example" {
  name    = "my-network"
  cidr    = "10.0.0.0/24"
  gateway = "10.0.0.1"
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

resource "prodata_volume" "data" {
  name = "my-data-volume"
  type = "SSD"
  size = 100
}

resource "prodata_volume_attachment" "data" {
  vm_id     = prodata_vm.example.id
  volume_id = prodata_volume.data.id
}
```

## Schema

### Required

- `vm_id` (Number) The ID of the virtual machine to attach the volume to. Changing this forces a new resource.
- `volume_id` (Number) The ID of the volume to attach. Changing this forces a new resource.

### Optional

- `region` (String) Region ID override. If not specified, uses the provider's default region. Changing this forces a new resource.
- `project_tag` (String) Project tag override. If not specified, uses the provider's default project_tag. Changing this forces a new resource.

### Attribute Reference

- `attached_volume_id` (Number) The server-generated ID of the attached volume (VmDisk). This differs from `volume_id` and is computed upon successful attachment.

## Import

Volume attachments cannot be imported.
