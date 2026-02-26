---
page_title: "prodata_public_ip Data Source - ProData Provider"
description: |-
  Lookup a ProData public IP by ID.
---

# prodata_public_ip (Data Source)

Lookup a ProData public IP by its unique identifier.

## Example Usage

```terraform
data "prodata_public_ip" "example" {
  id = 12345
}
```

## Schema

### Required

- `id` (Number) The unique identifier of the public IP.

### Optional

- `region` (String) Region ID override. If not specified, uses the provider's default region.
- `project_tag` (String) Project tag override. If not specified, uses the provider's default project tag.

### Attribute Reference

- `name` (String) The name of the public IP.
- `ip` (String) The allocated public IP address.
- `mask` (String) The subnet mask of the public IP.
- `gateway` (String) The gateway IP address.
