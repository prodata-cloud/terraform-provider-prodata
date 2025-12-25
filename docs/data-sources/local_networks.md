---
page_title: "prodata_local_networks Data Source - ProData Provider"
description: |-
  List all available ProData local networks.
---

# prodata_local_networks (Data Source)

List all available ProData local networks in a project.

## Example Usage

```terraform
data "prodata_local_networks" "all" {}
```

## Schema

### Optional

- `region` (String) Region ID override. If not specified, uses the provider's default region.
- `project_id` (Number) Project ID override. If not specified, uses the provider's default project ID.

### Read-Only

- `local_networks` (List of Object) List of available local networks. Each local network has the following attributes:
  - `id` (Number) The unique identifier of the local network.
  - `name` (String) The name of the local network.
  - `cidr` (String) The CIDR block of the local network.
  - `gateway` (String) The gateway IP address of the local network.
  - `linked` (Boolean) `true` if the local network is linked to an instance, `false` otherwise.
