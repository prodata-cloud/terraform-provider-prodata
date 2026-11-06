---
page_title: "prodata_security_group_attachment Resource - ProData Provider"
subcategory: "Networking"
description: |-
  Binds a security group to a VM interface.
---

# prodata_security_group_attachment (Resource)

Binds a security group to one VM interface. The VM and group must be in the selected project, and the VM must be in the selected region. The group must have at least one rule. Only one security group may manage an interface; a conflicting existing binding fails instead of being replaced.

Destroying the resource detaches this binding and clears its group rules using the platform's existing firewall behavior. The interface remains on the VM. Local interfaces have their firewall flag disabled where the platform supports that operation; public interfaces retain their firewall flag and IP filter. A binding changed to another group outside Terraform is never cleared by this resource's delete operation.

Requires the platform's `/api/v2/security-groups` API and `SECURITY_GROUPS` read/write permissions. ASG member interfaces remain controlled by the ASG and cannot be manually changed through this resource.

## Example Usage

```terraform
resource "prodata_security_group" "private" {
  name = "private"
  rules = [
    { direction = "in", action = "ACCEPT", source = "192.0.2.0/24" },
    { direction = "in", action = "DROP" },
    { direction = "out", action = "ACCEPT" }
  ]
}

resource "prodata_security_group_attachment" "private" {
  security_group_id = prodata_security_group.private.id
  vm_id             = prodata_vm.example.id
  interface_id      = prodata_local_network_attachment.private.interface_id
}
```

`interface_id` is the stable `vm_nets` row ID returned by the platform's VM interface API (or `prodata_local_network_attachment.interface_id`). It is not the `net0`/`net1` index, which can be reused after removal.

## Schema

### Required

- `security_group_id` (Number) Positive security group ID. Changing it replaces the attachment.
- `vm_id` (Number) Positive platform VM ID. Changing it replaces the attachment.
- `interface_id` (Number) Positive, stable VM interface row ID. Changing it replaces the attachment.

### Optional

- `region` (String) VM region context. Defaults to the provider region. Changing it replaces the attachment.
- `project_tag` (String) Owning project of the VM and group. Defaults to the provider project. Changing it replaces the attachment.

### Read-Only

- `id` (String) Composite `group_id/vm_id/interface_id` identifier.

## Import

Configure the provider with the matching project and region, then import the complete binding identity:

```shell
terraform import prodata_security_group_attachment.private 42/123/456
```
