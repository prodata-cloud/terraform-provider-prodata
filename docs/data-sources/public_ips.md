---
page_title: "prodata_public_ips Data Source - ProData Provider"
description: |-
  List all available ProData public IPs.
---

# prodata_public_ips (Data Source)

List all available ProData public IPs in a project.

## Example Usage

```terraform
data "prodata_public_ips" "all" {}
```

## Schema

### Optional

- `region` (String) Region ID override. If not specified, uses the provider's default region.
- `project_tag` (String) Project tag override. If not specified, uses the provider's default project tag.

### Attribute Reference

- `public_ips` (List of Object) List of available public IPs. Each public IP has the following attributes:
  - `id` (Number) The unique identifier of the public IP.
  - `name` (String) The name of the public IP.
  - `ip` (String) The allocated public IP address.
  - `mask` (String) The subnet mask of the public IP.
  - `gateway` (String) The gateway IP address.
