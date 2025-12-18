---
page_title: "prodata_image Data Source - ProData Provider"
subcategory: "VM"
description: |-
  Get information about a ProData image (OS template or custom image) for use in other resources.
---

# prodata_image (Data Source)

Use this data source to retrieve information about a ProData image. Images can be either OS templates (like Ubuntu, Debian, CentOS) or custom images that you've created.

## Example Usage

### Lookup by Slug (OS Template)

```terraform
data "prodata_image" "ubuntu" {
  slug = "ubuntu-22.04"
}

# Use the image ID in a VM resource
resource "prodata_vm" "example" {
  name     = "my-server"
  image_id = data.prodata_image.ubuntu.id
  # ... other configuration
}
```

### Lookup by Name (Custom Image)

```terraform
data "prodata_image" "my_custom_image" {
  name = "my-custom-web-server"
}

output "image_info" {
  value = {
    id        = data.prodata_image.my_custom_image.id
    is_custom = data.prodata_image.my_custom_image.is_custom
  }
}
```

## Argument Reference

The following arguments are supported. **Note:** You must specify either `slug` or `name`, but not both.

- `slug` - (Optional) The slug of the image. Used for OS template lookup (e.g., `ubuntu-22.04`, `debian-11`, `centos-8`). Mutually exclusive with `name`.
- `name` - (Optional) The name of the image. Used for custom images lookup. Mutually exclusive with `slug`.

## Attribute Reference

In addition to all arguments above, the following attributes are exported:

- `id` - The unique identifier of the image.
- `is_custom` - Boolean indicating whether this is a custom image (`true`) or an OS template (`false`).

## Common OS Template Slugs

Here are some commonly used OS template slugs:

- `ubuntu-22.04`  - Ubuntu 22.04 LTS
- `ubuntu-20.04`  - Ubuntu 20.04 LTS
- `debian-11`     - Debian 11 (Bullseye)
- `debian-12`     - Debian 12 (Bookworm)

> **Note:** Available OS templates may vary by region. Check your ProData Cloud console for the complete list of available images.
