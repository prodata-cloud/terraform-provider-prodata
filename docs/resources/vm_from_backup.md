---
page_title: "prodata_vm_from_backup Resource - ProData Provider"
subcategory: "Compute"
description: |-
  Restores a backup as a separate ProData VM.
---

# prodata_vm_from_backup (Resource)

Restores a successful backup as a new VM in the backup's source project and region. The source VM is preserved. Requires the VM protection v2 API and the platform restore worker.

## Example Usage

```terraform
resource "prodata_vm_from_backup" "recovery" {
  backup_id   = prodata_vm_backup.daily_point.id
  name        = "recovered-server"
  cpu_cores   = 2
  ram         = 16
  description = "Recovery copy"
  timeouts    = { create = "90m" }
}
```

## Behavior

The platform restores the archive onto SSD storage. Disk sizes come from the backup's saved configuration, including data disks. CPU and RAM use the explicit resource inputs. The platform creates a stopped VM, removes copied network interfaces and SSH keys, and changes its cloud-init password. Configure its networking and guest access before starting it; this resource does not claim an attached network or public IP. Backups of guests without cloud-init retain their guest credentials.

Creation waits for the restored VM to become `STOPPED` or `RUNNING`. A failure after a durable VM ID is returned preserves the ID in state, so the incomplete VM can be inspected and removed. An existing VM name causes a conflict, even if its backup and specification match; taking ownership requires explicit import. The API reserves and commits the VM and disk metadata before submission. Terraform records this canonical ID before calling submit. A lost submit reply is reconciled by that same ID; no second restore is queued. An `UNVERIFIED` outcome retains the ID and blocks deletion/resubmission until reconciliation. A lost reservation reply creates no upstream VM and requires inspection/import or cancellation of that unused reservation. The one-time generated password can be unavailable after a lost response or import.

The generated password is sensitive but is stored in Terraform state. Protect state accordingly. A generated password only configures cloud-init; it does not verify guest login.

Name, CPU, RAM and description update the same VM in place. CPU/RAM changes stop and restart a running VM; a stopped VM stays stopped. Name/description changes do not stop the guest. Only backup identity, region and project require replacement. Destroying this resource deletes the restored VM and its disks; it does not delete its source backup or source VM. Separate resources may attach networks or public IPs to the restored VM. Keep the backup while this resource is managed.

## Schema

### Required

- `backup_id` (Number) Successful backup ID. Changes replace the restored VM.
- `name` (String) VM name: 3–63 letters, digits and hyphens, including a letter. Updated in place.
- `cpu_cores` (Number) Positive CPU core count. Updated in place.
- `ram` (Number) RAM in GB. Converted exactly to MiB for the restore worker. Updated in place.

### Optional

- `description` (String) VM description. Updated in place.
- `region` (String) Source backup's region; defaults to the provider. Updated in place.
- `project_tag` (String) Source backup's project; defaults to the provider. Updated in place.
- `timeouts` (Object) `create` and `delete` default to `60m`, `read` to `5m`, `update` to `30m`.

### Read-Only

- `id` (Number) Restored VM ID.
- `restart_pending` (Boolean) Whether a start after a resource update must be retried.
- `status` (String) Latest platform VM status.
- `password` (String, Sensitive) One-time generated cloud-init password; may be null after recovery/import.

## Import

Use the source backup ID and the restored VM ID:

```shell
terraform import prodata_vm_from_backup.recovery 456:789
```
