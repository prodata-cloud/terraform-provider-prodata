---
page_title: "prodata_vm_backup_schedule Resource - ProData Provider"
subcategory: "Compute"
description: |-
  Manages a ProData vm backup schedule.
---

# prodata_vm_backup_schedule (Resource)

Manages a vm backup schedule through the project-scoped v2 API. Requires a panel with the VM protection API; an older panel returns an error and existing Terraform state is retained.

## Example Usage

```terraform
resource "prodata_vm_backup_schedule" "nightly" {
  vm_id = prodata_vm.example.id
  name  = "nightly"
  cron  = "0 0 2 * * *"
}
```

## Behavior

Schedule creation registers the existing platform scheduler. Cron has six Spring fields (seconds first) and runs in `Asia/Tashkent`, UTC+05:00. Retention follows the account's scheduled-backup limit. Deleting a schedule stops future runs and preserves successful backups. Name, cron and mode can be updated in place.

Names are unique per source VM and object kind. An existing same-name object causes a conflict, even when its specification matches; use explicit import to take ownership. Use a separate name for every independent backup or snapshot. After an upstream response is lost before the panel saved the job, inspect the platform before submitting another operation.

A missing object is removed from state only after the VM protection API explicitly confirms its absence. HTTP errors, missing routes and invalid identity responses retain state. Destroy waits for a running backup/snapshot to finish, then checks deletion; a deletion that cannot be confirmed retains state.

## Schema

### Required

- `vm_id` (Number) Source VM ID in the selected project and region. Changes replace the object.
- `name` (String) Name with 3–63 letters, digits, underscores or periods; backup names also allow hyphens. Can be updated in place.
- `cron` (String) Six-field Spring cron expression, for example `0 0 2 * * *`. Can be updated in place.

### Optional

- `mode` (String) `snapshot` (default). The current backup service supports snapshot mode only.
- `region` (String) Region override; defaults to the provider. Changes replace the object.
- `project_tag` (String) Project override; defaults to the provider. Changes replace the object.
- `timeouts` (Object) Operation timeouts; `create`/`delete` default to `60m`, `read`/`update` to `5m`. Use attributes such as `timeouts = { create = "90m" }`.

### Read-Only

- `id` (Number) Persistent panel object ID.
- `status` (String) Latest platform status; successful backup/snapshot is `SUCCESS`, active schedule is `SCHEDULE`.

## Import

Use the source VM ID and panel object ID. The provider's selected scope must match:

```shell
terraform import prodata_vm_backup_schedule.example 123:456
```
