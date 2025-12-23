---
page_title: "prodata_volume Resource - ProData Provider"
description: |-
  Manages a ProData volume.
---

# prodata_volume (Resource)

Manages a ProData volume.

~> **Note:** The ProData API does not support volume deletion. When you destroy this resource, the volume will be removed from Terraform state but will continue to exist in ProData.

## Example Usage

### Basic Usage

```terraform
resource "prodata_volume" "example" {
  region     = "UZ5"
  project_id = 89
  name       = "my-volume"
  type       = "HDD"
  size       = 10
}

output "volume_id" {
  value = prodata_volume.example.id
}
```

### SSD Volume

```terraform
resource "prodata_volume" "ssd" {
  region     = "UZ5"
  project_id = 89
  name       = "fast-storage"
  type       = "SSD"
  size       = 50
}
```

### Multiple Volumes

```terraform
resource "prodata_volume" "data" {
  region     = "UZ5"
  project_id = 89
  name       = "data-volume"
  type       = "HDD"
  size       = 100
}

resource "prodata_volume" "logs" {
  region     = "UZ5"
  project_id = 89
  name       = "logs-volume"
  type       = "HDD"
  size       = 50
}
```

## Schema

### Required

- `region` (String) Region where the volume will be created (e.g., UZ5). Changing this forces a new resource.
- `project_id` (Number) Project ID where the volume will be created. Changing this forces a new resource.
- `name` (String) The name of the volume.
- `type` (String) The type of the volume (HDD or SSD). Changing this forces a new resource.
- `size` (Number) The size of the volume in GB.

### Read-Only

- `id` (Number) The unique identifier of the volume.

## Import

Volumes cannot be imported as the API does not provide sufficient information to reconstruct the Terraform state.
