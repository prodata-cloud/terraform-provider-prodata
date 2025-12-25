---
page_title: "prodata_volume Resource - ProData Provider"
description: |-
  Manages a ProData volume.
---

# prodata_volume (Resource)

Manages a ProData volume.

~> **Note:** Only the `name` attribute can be updated in-place. Changing `type`, `size`, `region`, or `project_id` will force the creation of a new volume (destroy and recreate).

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
- `project_id` (Number) Project ID where the volume will be created. If not specified, uses the provider's default project_id. Changing this forces a new resource.

### Read-Only

- `id` (Number) The unique identifier of the volume.

## Import

Volumes cannot be imported as the API does not provide sufficient information to reconstruct the Terraform state.
