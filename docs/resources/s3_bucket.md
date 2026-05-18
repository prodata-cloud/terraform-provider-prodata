---
page_title: "prodata_s3_bucket Resource - ProData Provider"
description: |-
  Manages a ProData S3 (Ceph RGW) bucket.
---

# prodata_s3_bucket (Resource)

Manages a ProData S3 (Ceph RGW) bucket. Buckets are scoped to a single project; cross-project name conflicts surface as a clear error from the API.

Managing buckets requires the `S3:WRITE` permission on the API key used by the provider. Read-only data sources require `S3:READ`.

~> **Note:** `acl` and `versioning` are updated in-place via API calls. Changing `name`, `region`, `project_tag`, or `object_lock_enabled` forces resource replacement.

~> **Note:** `terraform destroy` only deletes an **empty** bucket. If the bucket still contains objects, versions, or in-progress multipart uploads, the API refuses the delete (HTTP 409) and the bucket is preserved — empty the bucket with an S3 client first, then re-run destroy.

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
  versioning = true
}
```

### Bucket with S3 object lock

```terraform
resource "prodata_s3_bucket" "locked" {
  name                = "my-locked-bucket"
  versioning          = true
  object_lock_enabled = true
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
- `versioning` (Boolean) Whether object versioning is enabled. Default: `false`. `true` enables versioning; `false` leaves a new bucket unversioned, or **suspends** versioning if it was previously enabled (S3 cannot fully remove versioning once enabled). Updated in place.
- `object_lock_enabled` (Boolean) Whether S3 object lock is enabled on the bucket. Default: `false`. Requires `versioning = true`. Cannot be changed after creation — changing this forces a new resource.

### Attribute Reference

- `id` (String) Resource identifier — equal to `name`.
- `creation_date` (String) Server-reported bucket creation timestamp (RFC3339).

## Import

Buckets are imported using a composite ID of the form `{region}/{name}@{project_tag}`:

```shell
terraform import prodata_s3_bucket.example UZ-5/my-bucket@my-project-42
```

~> **Note:** After import, `acl` is not refreshed from the server (trust-state — see note above). The first `terraform apply` after import issues a `PUT /acl` to reconcile your HCL value.

## Known Limitations

- **ACL drift is invisible.** If someone modifies the canned ACL via the AWS CLI / web console, Terraform will not detect or correct the change. Re-run `terraform apply` after any out-of-band ACL change to re-assert the configured value.
- **`terraform destroy` will not delete a non-empty bucket.** The provider never force-deletes bucket contents. If the bucket holds objects, versions, or multipart uploads, destroy fails with HTTP 409 and the bucket survives — empty it with an S3 client, then re-run destroy.
- **Versioning cannot be fully removed.** Once a bucket has had versioning enabled, S3 / Ceph RGW does not allow returning to the never-versioned state. Setting `versioning = false` on a previously-enabled bucket *suspends* versioning rather than removing it; a suspended bucket therefore also reads back as `versioning = false`.
- **Object lock is immutable.** `object_lock_enabled` can only be set at create time; toggling it forces resource replacement.
- **Bucket names are globally unique within the cluster.** The API returns a distinct error if the name is already taken by a bucket in another project.
