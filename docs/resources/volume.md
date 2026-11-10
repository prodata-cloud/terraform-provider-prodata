---
page_title: "prodata_volume Resource - ProData Provider"
subcategory: "Storage"
description: |-
  Manages a ProData volume.
---

# prodata_volume (Resource)

Manages a ProData volume.

The volume ID remains the same while the volume is attached to, detached from, or
moved between VMs. It is different from `prodata_volume_attachment.attached_volume_id`,
which identifies one attachment slot. Import this resource with the persistent volume ID.

This version requires the panel's stable volume API (`/api/v2/volumes/by-user-disk`,
identity version `user-disk-v1`). Deploy the matching API before upgrading the provider.
An older API or an unverified response produces an error and preserves existing state;
the provider never infers deletion from a volume missing from the legacy mixed-ID list.
Creation also requires the matching `volume-creation-v1` API. The provider saves a
`request_uuid` before submitting the reservation; the panel commits the canonical volume ID
before allocating storage. Lost replies are reconciled through that same request. A failed
creation can retain `request_uuid` while `id` is still null. Destroy first resolves/cancels that
exact request and retains state until cancellation or deletion is positively confirmed.
Read-only refresh never allocates a disk. Pending reservations remain discoverable in the
platform for recovery/import if the provider process terminates before returning state.
Creation checks API support before allocating a disk. Renaming an attached volume
targets the same persistent disk; a volume owned by an auto scaling group remains
managed by that group. Deletion requires the volume to be detached first.

The name and legacy identity assertion can be updated in place. Changing `type`, `size`, `region`, or `project_tag` forces the creation of a new volume (destroy and recreate).

## Example Usage

```terraform
resource "prodata_volume" "example" {
  name = "my-volume"
  type = "HDD"
  size = 10
}
```

## Schema

### Required

- `name` (String) The name of the volume. Updated in place.
- `type` (String) The type of the volume (HDD or SSD). Changing this forces a new resource.
- `size` (Number) The size of the volume in GB. Changing this forces a new resource.

### Optional

- `legacy_source_volume_id` (String) Confirmed full physical volume ID for a legacy hub disk without durable history. Apply this metadata before destroying the resource. A slot or name is insufficient, and the assertion cannot override recorded managed identity.

- `region` (String) Region where the volume will be created. If not specified, uses the provider's default region. Changing this forces a new resource.
- `project_tag` (String) Project tag where the volume will be created. If not specified, uses the provider's default project_tag. Changing this forces a new resource.

### Attribute Reference

- `id` (Number) The persistent canonical volume ID. May remain null while an unacknowledged creation is retained.
- `request_uuid` (String) Durable creation request identity, retained for recovery after a lost response. Existing legacy imports may have no request UUID.
- `status` (String) Verified creation or volume operation status.

## Import

Volumes can be imported using their ID:

```shell
terraform import prodata_volume.example <volume_id>
```

Example:

```shell
terraform import prodata_volume.example 123
```

### Import from another region or project

The import ID does not encode scope. Import uses the provider configuration selected by the
resource; resource-level `region` and `project_tag` arguments do not supply import scope.
For another region or project, configure a provider alias and select it before importing:

```terraform
provider "prodata" {
  alias       = "target"
  region      = "UZ-5"
  project_tag = "target-project"
}

resource "prodata_volume" "example" {
  provider = prodata.target
  name     = "existing-volume"
  type     = "SSD"
  size     = 10
}

import {
  to = prodata_volume.example
  id = "456"
}
```

Use the existing volume's name, type, and size. Configure the API endpoint and credentials
as usual, for example through `PRODATA_API_BASE_URL`, `PRODATA_API_KEY_ID`, and
`PRODATA_API_SECRET_KEY`. The same resource configuration also scopes the CLI command
`terraform import prodata_volume.example 456`. Import reads the exact existing ID; it does
not create a volume or adopt one by name. Review the plan before applying other changes.
