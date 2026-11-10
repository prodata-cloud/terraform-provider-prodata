---
page_title: "prodata_volume_attachment Resource - ProData Provider"
subcategory: "Storage"
description: |-
  Attaches a ProData volume to a virtual machine.
---

# prodata_volume_attachment (Resource)

Attaches a ProData volume to a virtual machine. Destroying this resource detaches the volume from the VM.

~> **Note:** Changing the VM, volume, region, or project requires replacement.

~> **Note:** If the VM is running when this resource is destroyed, the provider will automatically stop the VM, detach the volume, and leave the VM stopped.

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

- `legacy_source_volume_id` (String) Explicit physical ID of a legacy hub volume when no managed identity history exists. Omit for managed volumes.
- `legacy_attached_volume_id` (String) Explicit physical ID of an imported legacy attachment when no managed identity history exists. Apply this assertion before destroying the attachment. A slot, display name, or observed candidate is not an identity assertion. A supplied value cannot override a known managed identity.
- `region` (String) Region ID override. If not specified, uses the provider's default region. Changing this forces a new resource.
- `project_tag` (String) Project tag override. If not specified, uses the provider's default project_tag. Changing this forces a new resource.

### Attribute Reference

- `attached_volume_id` (Number) The server-generated reservation and attachment ID (VmDisk), saved before the disk is moved. This differs from `volume_id`.
- `status` (String) Verified attachment operation status. Pending or unverified moves retain their identity for recovery.

Create and destroy require the versioned `volume-attachment-v1` API. A reserved ID is saved before submission. A lost response is reconciled through that exact ID; an uncertain physical move is never repeated automatically. Refresh retains uncertain attachments with a warning.

## Import

Volume attachments can be imported using `vm_id:volume_id`:

```shell
terraform import prodata_volume_attachment.example <vm_id>:<volume_id>
```

Example:

```shell
terraform import prodata_volume_attachment.example 123:456
```

~> **Note:** During import, the provider makes an API call to resolve the `attached_volume_id` from the given `volume_id`. The volume must already be attached to the VM.

### Import from another region or project

The `vm_id:volume_id` import ID does not encode scope. Import uses the provider configuration
selected by the resource, including both region and project. Resource-level `region` and
`project_tag` arguments do not supply import scope. Select a matching provider alias first:

```terraform
provider "prodata" {
  alias       = "target"
  region      = "UZ-5"
  project_tag = "target-project"
}

resource "prodata_volume_attachment" "example" {
  provider  = prodata.target
  vm_id     = 123
  volume_id = 456
}

import {
  to = prodata_volume_attachment.example
  id = "123:456"
}
```

Configure the API endpoint and credentials as usual, for example through
`PRODATA_API_BASE_URL`, `PRODATA_API_KEY_ID`, and `PRODATA_API_SECRET_KEY`. This resource
configuration also scopes `terraform import prodata_volume_attachment.example 123:456`.
Import reads the exact existing VM/volume binding; it does not attach a disk, restart the
VM, or adopt another attachment by name. Review the plan before applying other changes.
