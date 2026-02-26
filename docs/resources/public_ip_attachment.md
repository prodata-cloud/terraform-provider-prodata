---
page_title: "prodata_public_ip_attachment Resource - ProData Provider"
description: |-
  Attaches a ProData public IP to a virtual machine.
---

# prodata_public_ip_attachment (Resource)

Attaches a ProData public IP to a virtual machine. Destroying this resource detaches the public IP from the VM.

~> **Note:** All attributes require resource replacement when changed. Any change will detach and reattach the public IP.

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
}
```

## Schema

### Required

- `vm_id` (Number) The ID of the virtual machine to attach the public IP to. Changing this forces a new resource.
- `public_ip_id` (Number) The ID of the public IP to attach. Changing this forces a new resource.

### Optional

- `region` (String) Region ID override. If not specified, uses the provider's default region. Changing this forces a new resource.
- `project_tag` (String) Project tag override. If not specified, uses the provider's default project_tag. Changing this forces a new resource.

### Attribute Reference

- `public_ip` (String) The public IP address string assigned to the VM.

## Import

Public IP attachments cannot be imported.
