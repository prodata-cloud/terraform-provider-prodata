---
page_title: "prodata_local_network Data Source - ProData Provider"
subcategory: "Networking"
description: |-
  Lookup a ProData local network by ID or by name.
---

# prodata_local_network (Data Source)

Lookup a ProData local network by its unique identifier or by its name.

Looking up by `name` lists the local networks of the selected region and project and matches the
name exactly (case-sensitive). It fails if no network has that name, and also if more than one
does — the panel does not enforce unique names on every path (for example, renaming a network
through the API can produce a duplicate) — use `id` in that case.

## Example Usage

Look up by name:

```terraform
data "prodata_local_network" "by_name" {
  name = "my-network"
}
```

Look up by ID:

```terraform
data "prodata_local_network" "by_id" {
  id = 12345
}
```

## Schema

### Optional

Exactly one of `id` or `name` must be specified.

- `id` (Number) The unique identifier of the local network. Conflicts with `name`. If you look the network up by `name`, this is populated from the API.
- `name` (String) The name of the local network. Matched exactly (case-sensitive) among the networks of the selected region and project. Conflicts with `id`. If you look the network up by `id`, this is populated from the API.
- `region` (String) Region ID override. If not specified, uses the provider's default region.
- `project_tag` (String) Project tag override. If not specified, uses the provider's default project tag.

### Attribute Reference

- `cidr` (String) The CIDR block of the local network.
- `gateway` (String) The gateway IP address of the local network.
- `linked` (Boolean) `true` if the local network is linked to an instance, `false` otherwise.
