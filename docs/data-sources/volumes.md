---
page_title: "prodata_volumes Data Source - ProData Provider"
subcategory: "Storage"
description: |-
  List all available ProData volumes.
---

# prodata_volumes (Data Source)

IDs in this list are persistent volume IDs, including for attached volumes. They can
be used as `prodata_volume_attachment.volume_id` and imported as `prodata_volume`.
This requires the panel's stable volume API (`user-disk-v1`); attachment slot IDs are
not substituted for volume IDs.

`unverified_volume_ids` lists old platform records whose deleted flag cannot be
distinguished from a lost attachment. They are excluded from the verified `volumes`
list and require inspection. Reading an individual unverified volume produces an
error and preserves its resource state.

List all available ProData volumes in a project.

## Example Usage

```terraform
data "prodata_volumes" "all" {}
```

## Schema

### Optional

- `region` (String) Region ID override. If not specified, uses the provider's default region.
- `project_tag` (String) Project tag override. If not specified, uses the provider's default project tag.

### Attribute Reference

- `unverified_volume_ids` (List of Number) Persistent IDs whose existence cannot be verified from legacy platform records.

- `volumes` (List of Object) List of available volumes. Each volume has the following attributes:
  - `id` (Number) The unique identifier of the volume.
  - `name` (String) The name of the volume.
  - `type` (String) The type of the volume (e.g., HDD, SSD).
  - `size` (Number) The size of the volume in GB.
  - `in_use` (Boolean) `true` if the volume is attached to an instance, `false` otherwise.
  - `attached_id` (Number) The ID of the instance the volume is attached to, or `null` if not attached.
