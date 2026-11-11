---
page_title: "prodata_local_network Resource - ProData Provider"
subcategory: "Networking"
description: |-
  Manages a ProData local network.
---

# prodata_local_network (Resource)

Manages a ProData local network.

Creating a network whose name is already in use fails with API error `614`, even when
the existing network has the same CIDR and gateway. To manage an existing network,
verify its ID and [import it explicitly](#import), or choose a different name for the
new network. If a previous creation was interrupted and the network already exists,
use the same explicit import procedure after checking the result in the platform.

~> **Note:** Only the `name` attribute can be updated in-place. Changing `cidr`, `gateway`, `region`, or `project_tag` will force the creation of a new local network (destroy and recreate). The platform refuses to delete a network that has VMs in it, so move the VMs to another network first: see [Moving a VM to another network](vm.md#moving-a-vm-to-another-network).

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

### Attribute Reference

- `id` (Number) The unique identifier of the local network.

## Import

Local networks can be imported using their ID:

```shell
terraform import prodata_local_network.example <network_id>
```

Example:

```shell
terraform import prodata_local_network.example 123
```
