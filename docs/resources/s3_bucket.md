---
page_title: "prodata_s3_bucket Resource - ProData Provider"
description: |-
  Manages a ProData S3 (Ceph RGW) bucket.
---

# prodata_s3_bucket (Resource)

Manages a ProData S3 (Ceph RGW) bucket. Buckets are scoped to a single project; cross-project name conflicts surface as a clear error from the API.

~> **Note:** `acl`, `versioning`, and `force_destroy` are updated in-place. Changing `name`, `region`, `project_tag`, or `object_lock_enabled` forces resource replacement (destroy and recreate).

~> **Note:** `acl` is **trust-state only** — it is sent to the server on Create/Update but never re-read. The S3 ACL API exposes grants, not canned ACLs, so the original canned value cannot be reliably round-tripped. Changes made outside Terraform will not produce a plan diff. `versioning` and `object_lock_enabled` are drift-detected normally.

## Example Usage

```terraform
resource "prodata_s3_bucket" "example" {
  name = "my-bucket"
}
```

### Versioned bucket

```terraform
resource "prodata_s3_bucket" "versioned" {
  name       = "my-versioned-bucket"
  acl        = "private"
  versioning = "enabled"
}
```

### Bucket with S3 object lock

```terraform
resource "prodata_s3_bucket" "locked" {
  name                = "my-locked-bucket"
  versioning          = "enabled"
  object_lock_enabled = true
}
```

### Ephemeral bucket (destroyed on `terraform destroy` even if non-empty)

```terraform
resource "prodata_s3_bucket" "ephemeral" {
  name          = "my-ephemeral-bucket"
  force_destroy = true
}
```

## Schema

### Required

- `name` (String) Bucket name. 3-24 characters; lowercase letters, digits, dots and hyphens; no leading/trailing or consecutive separators. Changing this forces a new resource.

### Optional

- `region` (String) Region ID where the bucket will be created. If omitted, uses the provider's default region. Changing this forces a new resource.
- `project_tag` (String) Project tag the bucket belongs to. If omitted, uses the provider's default project_tag. Changing this forces a new resource.
- `acl` (String) Canned ACL: `private`, `public-read`, or `public-read-write`. Default: `private`. Updated in place. **Not drift-detected** (see note above).
- `versioning` (String) Versioning state: `enabled`, `suspended`, or `disabled`. Default: `disabled`. Once a bucket has transitioned out of `disabled`, it cannot return to `disabled` — the only legal transition pair afterwards is `enabled` ↔ `suspended`.
- `object_lock_enabled` (Boolean) Whether S3 object lock is enabled on the bucket. Default: `false`. Requires `versioning = "enabled"`. Cannot be changed after creation — changing this forces a new resource.
- `force_destroy` (Boolean) Default: `false`. If `true`, `terraform destroy` wipes all objects, versions, and multipart uploads inside the bucket before deleting it. If `false`, destroy refuses on a non-empty bucket (HTTP 409) and the bucket is preserved. State-only — not refreshed from the server.

### Attribute Reference

- `id` (String) Resource identifier — equal to `name`.
- `creation_date` (String) Server-reported bucket creation timestamp (ISO-8601).

## Import

Buckets are imported using a composite ID of the form `{region}/{name}@{project_tag}`:

```shell
terraform import prodata_s3_bucket.example UZ-5/my-bucket@my-project
```

~> **Note:** After import, `acl` and `force_destroy` are not populated from the server (both are state-only attributes). The next `terraform plan` will show them as drift against your configuration; the next `terraform apply` will reconcile them to the values declared in HCL with no server-side side effect for `force_destroy`, and a fresh PUT for `acl`.

## Known Limitations

- **ACL drift is invisible.** If someone modifies the canned ACL via the AWS CLI / web console, Terraform will not detect or correct the change. Re-run `terraform apply` after any out-of-band ACL change to re-assert the configured value.
- **`force_destroy` is state-only.** Toggling `force_destroy` produces a state-only diff (no API call); the value only matters at `terraform destroy` time.
- **Versioning is monotonic.** A bucket created with `versioning = "disabled"` (the default) can be moved to `enabled` or `suspended` later, but `enabled`/`suspended` cannot be moved back to `disabled`. This mirrors the underlying S3 / Ceph RGW behavior.
- **Object lock is immutable.** `object_lock_enabled` can only be set at create time; toggling it forces resource replacement.
- **Bucket names are globally unique within the cluster.** The API returns a distinct error if the name is already taken by a bucket in another project.
