---
page_title: "prodata_volume Data Source - ProData Provider"
description: |-
  Lookup a ProData volume by ID.
---

# prodata_volume (Data Source)

Lookup a ProData volume by its unique identifier.

## Example Usage

```terraform
data "prodata_volume" "example" {
  id = 12345
}
```

## Schema

### Required

- `id` (Number) The unique identifier of the volume.

### Optional

- `region` (String) Region ID override. If not specified, uses the provider's default region.
- `project_tag` (String) Project tag override. If not specified, uses the provider's default project tag.

### Read-Only

- `name` (String) The name of the volume.
- `type` (String) The type of the volume (e.g., HDD, SSD).
- `size` (Number) The size of the volume in GB.
- `in_use` (Boolean) `true` if the volume is attached to an instance, `false` otherwise.
- `attached_id` (Number) The ID of the instance the volume is attached to, or `null` if not attached.
