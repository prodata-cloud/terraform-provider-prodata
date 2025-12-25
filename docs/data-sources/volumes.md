---
page_title: "prodata_volumes Data Source - ProData Provider"
description: |-
  List all available ProData volumes.
---

# prodata_volumes (Data Source)

List all available ProData volumes in a project.

## Example Usage

```terraform
data "prodata_volumes" "all" {}
```

## Schema

### Optional

- `region` (String) Region ID override. If not specified, uses the provider's default region.
- `project_id` (Number) Project ID override. If not specified, uses the provider's default project ID.

### Read-Only

- `volumes` (List of Object) List of available volumes. Each volume has the following attributes:
  - `id` (Number) The unique identifier of the volume.
  - `name` (String) The name of the volume.
  - `type` (String) The type of the volume (e.g., HDD, SSD).
  - `size` (Number) The size of the volume in GB.
  - `in_use` (Boolean) `true` if the volume is attached to an instance, `false` otherwise.
  - `attached_id` (Number) The ID of the instance the volume is attached to, or `null` if not attached.
