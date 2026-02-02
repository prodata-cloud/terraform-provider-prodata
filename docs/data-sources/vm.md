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
- `status` (String) The current status of the virtual machine (RUNNING, STOPPED, etc.).
- `cpu_cores` (Number) The number of CPU cores.
- `ram` (Number) The amount of RAM in GB.
- `disk_size` (Number) The size of the disk in GB.
- `disk_type` (String) The type of disk (HDD, SSD, or NVME).
- `private_ip` (String) The private IP address of the virtual machine.
- `public_ip` (String) The public IP address assigned to the virtual machine (if any).
- `local_network_id` (Number) The ID of the local network the VM is attached to.
- `description` (String) Description of the virtual machine.
