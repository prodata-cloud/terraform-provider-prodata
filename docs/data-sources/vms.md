---
page_title: "prodata_vms Data Source - ProData Provider"
description: |-
  List all available ProData virtual machines.
---

# prodata_vms (Data Source)

List all available ProData virtual machines in a project.

## Example Usage

```terraform
data "prodata_vms" "all" {}
```

## Schema

### Optional

- `region` (String) Region ID override. If not specified, uses the provider's default region.
- `project_tag` (String) Project tag override. If not specified, uses the provider's default project tag.

### Read-Only

- `vms` (List of Object) List of available virtual machines. Each virtual machine has the following attributes:
  - `id` (Number) The unique identifier of the virtual machine.
  - `name` (String) The name of the virtual machine.
