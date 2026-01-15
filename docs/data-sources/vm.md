---
page_title: "prodata_vm Data Source - ProData Provider"
description: |-
  Lookup a ProData virtual machine by ID.
---

# prodata_vm (Data Source)

Lookup a ProData virtual machine by its unique identifier.

## Example Usage

```terraform
data "prodata_vm" "example" {
  id = 12345
}
```

## Schema

### Required

- `id` (Number) The unique identifier of the virtual machine.

### Optional

- `region` (String) Region ID override. If not specified, uses the provider's default region.
- `project_tag` (String) Project tag override. If not specified, uses the provider's default project tag.

### Read-Only

- `name` (String) The name of the virtual machine.
