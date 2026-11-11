---
page_title: "prodata_security_group Resource - ProData Provider"
subcategory: "Networking"
description: |-
  Manages a security group and its ordered firewall rules.
---

# prodata_security_group (Resource)

Manages a security group and its complete ordered ruleset. Renaming the group or changing rules updates it in place, including its attached VM interfaces. Requires the platform's `/api/v2/security-groups` API and `SECURITY_GROUPS` read/write permissions.

Rules are authoritative: changes made outside Terraform are detected and replaced on the next apply. Keep all rules for one group in this resource. Rules are evaluated in list order within each direction; an early catch-all can hide rules below it. Include explicit final rules for your desired incoming and outgoing policy. An empty group (`rules = []`) can be created but cannot be attached to a VM. A group with active attachments cannot be deleted or emptied.

Groups belong to a project, while their names are unique within the company. These endpoints manage the owning project; a group merely shared from another project must be managed in its owning project. Region is the request context; groups themselves are project-wide. Changing `region` or `project_tag` replaces the Terraform resource.

## Example Usage

```terraform
resource "prodata_security_group" "web" {
  name = "web"

  rules = [
    {
      direction        = "in"
      action           = "ACCEPT"
      protocol         = "tcp"
      source           = "192.0.2.0/24"
      destination_port = "443"
    },
    { direction = "in", action = "DROP" },
    { direction = "out", action = "ACCEPT" }
  ]
}
```

## Schema

### Required

- `name` (String) Nonblank group name, up to 100 characters, unique within the company.
- `rules` (List of Object) Complete ordered ruleset, up to 200 rules. Each rule supports the attributes below. An empty list creates an unattached group.

### Optional

- `region` (String) Region context. Defaults to the provider region. Changing it replaces the resource.
- `project_tag` (String) Owning project. Defaults to the provider project. Changing it replaces the resource.

### Read-Only

- `id` (Number) Security group ID.

### Nested Schema for `rules`

Required:

- `direction` (String) `in` or `out`.
- `action` (String) `ACCEPT`, `DROP`, or `REJECT`.

Optional (default `""`):

- `protocol` (String) `tcp`, `udp`, `icmp`, `ipv6-icmp`, or empty for any protocol.
- `source` (String) Literal IPv4/IPv6 address or CIDR; empty matches any source.
- `destination` (String) Literal IPv4/IPv6 address or CIDR; empty matches any destination.
- `source_port` (String) Source port, ascending range such as `1024:65535`, or comma-separated list. Ports must be 1–65535 and require `tcp` or `udp`; empty matches any port.
- `destination_port` (String) Destination port, range, or list, with the same rules as `source_port`.

## Import

Configure the provider with the group's owning project and region context, then import by ID:

```shell
terraform import prodata_security_group.web 42
```

Import reads the existing rules. Copy them into configuration before applying; an empty or different configured ruleset will replace them.

## Operation failures

Rule changes are pushed to each attached VM. If a downstream update fails after some interfaces were updated, apply reports an error and the platform retains the previous stored ruleset. Apply again to converge every interface to the desired complete ruleset. Operations do not claim a change succeeded when a downstream firewall operation failed.

A missing API route on an older server is an error and preserves Terraform state. Only the scoped API's explicit not-found response is treated as deletion.
