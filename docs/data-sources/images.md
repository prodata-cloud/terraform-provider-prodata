---
page_title: "prodata_images Data Source - ProData Provider"
description: |-
  List all available ProData images (OS templates and custom images).
---

# prodata_images (Data Source)

List all available ProData images including OS templates and custom images.

## Example Usage

```terraform
data "prodata_images" "all" {}
```

## Schema

### Optional

- `region` (String) Region ID override. If not specified, uses the provider's default region.
- `project_tag` (String) Project tag override. If not specified, uses the provider's default project tag.

### Read-Only

- `images` (List of Object) List of available images. Each image has the following attributes:
  - `id` (Number) The unique identifier of the image.
  - `name` (String) The name of the image.
  - `slug` (String) The slug of the image.
  - `is_custom` (Boolean) `true` if this is a custom image, `false` if it's an OS template.
