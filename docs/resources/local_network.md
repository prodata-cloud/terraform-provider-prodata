---
page_title: "prodata_local_network Resource - ProData Provider"
description: |-
  Manages a ProData local network.
---

# prodata_local_network (Resource)

Manages a ProData local network.

~> **Note:** Only the `name` attribute can be updated in-place. Changing `cidr`, `gateway`, `region`, or `project_tag` will force the creation of a new local network (destroy and recreate).

## Example Usage

```terraform
resource "prodata_local_network" "example" {
  name    = "my-network"
  cidr    = "10.0.0.0/24"
  gateway = "10.0.0.1"
}
```

## Schema

### Required

- `name` (String) The name of the local network. **This is the only attribute that can be updated in-place.**
- `cidr` (String) The CIDR block for the local network (e.g., 10.0.0.0/24). Changing this forces a new resource.
- `gateway` (String) The gateway IP address for the local network (e.g., 10.0.0.1). Changing this forces a new resource.

### Optional

- `region` (String) Region where the local network will be created. If not specified, uses the provider's default region. Changing this forces a new resource.
- `project_tag` (String) Project tag where the local network will be created. If not specified, uses the provider's default project_tag. Changing this forces a new resource.

### Read-Only

- `id` (Number) The unique identifier of the local network.

## Import

Local networks cannot be imported as the API does not provide sufficient information to reconstruct the Terraform state.
