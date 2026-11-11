---
page_title: "prodata_vm_snapshot Resource - ProData Provider"
subcategory: "Compute"
description: |-
  Manages a ProData vm snapshot.
---

# prodata_vm_snapshot (Resource)

Manages a vm snapshot through the project-scoped v2 API. Requires a panel with the VM protection API; an older panel returns an error and existing Terraform state is retained.

## Example Usage

```terraform
resource "prodata_vm_snapshot" "before_change" {
  vm_id       = prodata_vm.example.id
  name        = "before_change"
  include_ram = false
}
```

## Behavior

Creation waits for the asynchronous operation to succeed. If polling fails or times out after a durable ID was returned, that ID remains in Terraform state; inspect the job before retrying or replacing it. Backups and snapshots are immutable; changes to their specification replace the object.

Names are unique per source VM and object kind. An existing same-name object causes a conflict, even when its specification matches; use explicit import to take ownership. Use a separate name for every independent backup or snapshot. After an upstream response is lost before the panel saved the job, inspect the platform before submitting another operation.

A missing object is removed from state only after the VM protection API explicitly confirms its absence. HTTP errors, missing routes and invalid identity responses retain state. Destroy waits for a running backup/snapshot to finish, then checks deletion; a deletion that cannot be confirmed retains state.

Snapshots belong to the source VM; they are not an independent off-VM backup. Names allow 3–63 letters, digits, underscores and periods.

## Schema

### Required

- `vm_id` (Number) Source VM ID in the selected project and region. Changes replace the object.
- `name` (String) Name with 3–63 letters, digits, underscores or periods; backup names also allow hyphens. Changes replace the object.

### Optional

- `include_ram` (Boolean) Include VM memory. Defaults to `false`; changes replace the snapshot.
- `region` (String) Region override; defaults to the provider. Changes replace the object.
- `project_tag` (String) Project override; defaults to the provider. Changes replace the object.
- `timeouts` (Object) Operation timeouts; `create`/`delete` default to `60m`, `read`/`update` to `5m`. Use attributes such as `timeouts = { create = "90m" }`.

### Read-Only

- `id` (Number) Persistent panel object ID.
- `status` (String) Latest platform status; successful backup/snapshot is `SUCCESS`, active schedule is `SCHEDULE`.

## Import

Use the source VM ID and panel object ID. The provider's selected scope must match:

```shell
terraform import prodata_vm_snapshot.example 123:456
```

Creation reserves and records a canonical panel ID before submitting the upstream job. A lost submit acknowledgement is reconciled by that exact ID and never queues a second job. `UNVERIFIED` means the platform cannot confirm the submission outcome: the ID is retained and deletion/resubmission is blocked until the operation is reconciled. An unsubmitted `RESERVED` object can be canceled safely. A lost reservation reply creates no upstream job; inspect and import or cancel that reservation explicitly. This requires `vm-protection-reservation-v1` capabilities, checked before mutations.
