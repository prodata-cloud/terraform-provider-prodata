---
page_title: "prodata_local_network Data Source - ProData Provider"
description: |-
  Lookup a ProData local network by ID.
---

# prodata_local_network (Data Source)

Lookup a ProData local network by its unique identifier.

## Example Usage

```terraform
data "prodata_local_network" "example" {
  id = 12345
}
```

## Schema

### Required

- `id` (Number) The unique identifier of the local network.

### Optional

- `region` (String) Region ID override. If not specified, uses the provider's default region.
- `project_id` (Number) Project ID override. If not specified, uses the provider's default project ID.

### Read-Only

- `name` (String) The name of the local network.
- `cidr` (String) The CIDR block of the local network.
- `gateway` (String) The gateway IP address of the local network.
- `linked` (Boolean) `true` if the local network is linked to an instance, `false` otherwise.
