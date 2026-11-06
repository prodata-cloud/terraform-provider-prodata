---
page_title: "prodata_volume Resource - ProData Provider"
subcategory: "Storage"
description: |-
  Manages a ProData volume.
---

# prodata_volume (Resource)

Manages a ProData volume.

The volume ID remains the same while the volume is attached to, detached from, or
moved between VMs. It is different from `prodata_volume_attachment.attached_volume_id`,
which identifies one attachment slot. Import this resource with the persistent volume ID.

This version requires the panel's stable volume API (`/api/v2/volumes/by-user-disk`,
identity version `user-disk-v1`). Deploy the matching API before upgrading the provider.
An older API or an unverified response produces an error and preserves existing state;
the provider never infers deletion from a volume missing from the legacy mixed-ID list.
Creation checks API support before allocating a disk. Renaming an attached volume
targets the same persistent disk; a volume owned by an auto scaling group remains
managed by that group. Deletion requires the volume to be detached first.

~> **Note:** Only the `name` attribute can be updated in-place. Changing `type`, `size`, `region`, or `project_tag` will force the creation of a new volume (destroy and recreate).

## Example Usage

```terraform
resource "prodata_volume" "example" {
  name = "my-volume"
  type = "HDD"
  size = 10
}
```

## Schema

### Required

- `name` (String) The name of the volume. **This is the only attribute that can be updated in-place.**
- `type` (String) The type of the volume (HDD or SSD). Changing this forces a new resource.
- `size` (Number) The size of the volume in GB. Changing this forces a new resource.

### Optional

- `region` (String) Region where the volume will be created. If not specified, uses the provider's default region. Changing this forces a new resource.
- `project_tag` (String) Project tag where the volume will be created. If not specified, uses the provider's default project_tag. Changing this forces a new resource.

### Attribute Reference

- `id` (Number) The unique identifier of the volume.

## Import

Volumes can be imported using their ID:

```shell
terraform import prodata_volume.example <volume_id>
```

Example:

```shell
terraform import prodata_volume.example 123
```
