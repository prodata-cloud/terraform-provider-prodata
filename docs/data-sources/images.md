---
page_title: "prodata_images Data Source - ProData Provider"
description: |-
  List all available ProData images (OS templates and custom images).
---

# prodata_images (Data Source)

List all available ProData images including OS templates and custom images.

## Example Usage

### List All Images

```terraform
data "prodata_images" "all" {}

output "all_images" {
  value = data.prodata_images.all.images
}
```

### With Region Override

```terraform
data "prodata_images" "kz_images" {
  region = "KZ-1"
}

output "kz_image_count" {
  value = length(data.prodata_images.kz_images.images)
}
```

### Filter Custom Images with Local

```terraform
data "prodata_images" "all" {}

locals {
  custom_images = [for img in data.prodata_images.all.images : img if img.is_custom]
  os_templates  = [for img in data.prodata_images.all.images : img if !img.is_custom]
}

output "custom_image_names" {
  value = [for img in local.custom_images : img.name]
}
```

## Schema

### Optional

- `region` (String) Region ID override. If not specified, uses the provider's default region.
- `project_id` (Number) Project ID override. If not specified, uses the provider's default project ID.

### Read-Only

- `images` (List of Object) List of available images. Each image has the following attributes:
  - `id` (Number) The unique identifier of the image.
  - `name` (String) The name of the image.
  - `slug` (String) The slug of the image.
  - `is_custom` (Boolean) `true` if this is a custom image, `false` if it's an OS template.
