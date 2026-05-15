---
page_title: "prodata_s3_bucket Resource - ProData Provider"
description: |-
  Manages a ProData S3 (Ceph RGW) bucket.
---

# prodata_s3_bucket (Resource)

Manages a ProData S3 (Ceph RGW) bucket. Buckets are scoped to a single project; cross-project name conflicts surface as a clear error from the API.

Managing buckets requires the `S3:WRITE` permission on the API key used by the provider. Read-only data sources require `S3:READ`.

~> **Note:** `acl` and `versioning` are updated in-place via API calls. `force_destroy` is a state-only flag (no API call) — its value only matters at `terraform destroy` time. Changing `name`, `region`, `project_tag`, or `object_lock_enabled` forces resource replacement.

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

## Accessing the bucket

Once created, a bucket is reachable via any S3-compatible client (awscli, mc, rclone, boto3, ...) using the regional S3 endpoint:

| Region     | S3 endpoint                          |
| ---------- | ------------------------------------ |
| Uzbekistan | `https://storage.pro-data.tech`      |
| Kazakhstan | `https://storage.kz-1.pro-data.tech` |

S3 access uses a **separate** `access_key` / `secret_key` pair — not the API key used by Terraform. Generate them under **Account** → **S3 Credentials** in the ProData Console.

Example with the AWS CLI:

```bash
aws --endpoint-url=https://storage.pro-data.tech \
    s3 cp ./file.txt s3://my-bucket/
```

Object-level operations (upload, download, list, delete), object lifecycle rules, bucket policies, and CORS configuration are **not** managed by this Terraform provider — use any S3-compatible client.

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
- `creation_date` (String) Server-reported bucket creation timestamp (RFC3339).

## Import

Buckets are imported using a composite ID of the form `{region}/{name}@{project_tag}`:

```shell
terraform import prodata_s3_bucket.example UZ-5/my-bucket@my-project-42
```

~> **Note:** After import:
> - `acl` is not refreshed from the server (trust-state — see note above). The first `terraform apply` after import issues a `PUT /acl` to reconcile your HCL value.
> - `force_destroy` is never server-side (state-only flag). The first apply after import records your HCL value into state with no API call.

## Known Limitations

- **ACL drift is invisible.** If someone modifies the canned ACL via the AWS CLI / web console, Terraform will not detect or correct the change. Re-run `terraform apply` after any out-of-band ACL change to re-assert the configured value.
- **`force_destroy` is state-only.** Toggling `force_destroy` produces a state-only diff (no API call); the value only matters at `terraform destroy` time.
- **Versioning is monotonic.** A bucket created with `versioning = "disabled"` (the default) can be moved to `enabled` or `suspended` later, but `enabled`/`suspended` cannot be moved back to `disabled`. This mirrors the underlying S3 / Ceph RGW behavior.
- **Object lock is immutable.** `object_lock_enabled` can only be set at create time; toggling it forces resource replacement.
- **Bucket names are globally unique within the cluster.** The API returns a distinct error if the name is already taken by a bucket in another project.
