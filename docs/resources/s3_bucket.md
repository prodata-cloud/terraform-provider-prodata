---
page_title: "prodata_s3_bucket Resource - ProData Provider"
subcategory: "Storage"
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

S3 clients authenticate with an **access key** generated under **Account** → **Access Keys** → **Generate Key** in the ProData Console. A key generated with an `S3` permission is both an API key and an S3 key: the value the console shows as **Access Key** (the provider's `api_key_id`) and the **Secret Key** are also the S3 access key ID and secret access key. That includes the key this provider uses (it needs `S3:WRITE` to manage buckets), so one key can serve Terraform and an S3 client, as long as the key's owner is the owner of the project — see [Key owners and project owners](#key-owners-and-project-owners). A key generated without an `S3` permission is not accepted by the S3 endpoint, and the permission cannot be added to an existing key.

Example with the AWS CLI:

```bash
export AWS_ACCESS_KEY_ID=<Access Key>
export AWS_SECRET_ACCESS_KEY=<Secret Key>
aws --endpoint-url=https://storage.pro-data.tech \
    s3 cp ./file.txt s3://my-bucket/
```

Object-level operations (upload, download, list, delete), object lifecycle rules, bucket policies, and CORS configuration are **not** managed by this Terraform provider — use any S3-compatible client.

### Key owners and project owners

An S3 key acts as its **owner**: the user the key was generated for. In the console only the organization's root user can generate keys, and it chooses that user in the required **Owner** field (also shown in the *Owner* column of the key list); a key generated through the API without an owner belongs to the user who generated it. Normally every user of an organization has their own S3 identity. A bucket created by this provider or in the console belongs to the identity of the **owner of the project** — the project's "canonical user" — whichever user or key made the request. The provider and the console reach the bucket on the project owner's behalf; an S3 client reaches it as the owner of its key. So:

- **A key owned by another user of your organization** (for example, a sub-user created for automation) is accepted by the S3 endpoint but acts as that other user. `ListBuckets` returns none of the project's buckets — an S3 listing contains only the caller's own buckets — and access to one of them fails with `403 AccessDenied` unless the bucket's ACL or a bucket policy allows it.
- **To work with the project's buckets over S3**, use a key whose owner is the project's owner (pick that user as **Owner** when generating the key). To let another user in, add a bucket policy that names that user's principal — over S3, with a key owned by the project's owner, since this provider does not manage bucket policies. The organization's root user finds a user's principal under **Account** → **User management** → **User Information** (the info button in the user's row), labelled *Principal for use when working with object storage*. A bucket shared this way still does not appear in the other user's `ListBuckets`; address it by name.
- Over S3 only bucket policies and ACLs apply. Neither the project and role rules of the console nor the key's `READ`/`WRITE` level are enforced there (the levels govern the panel API that this provider uses): any key with an `S3` permission, including a read-only one, can read, overwrite and delete everything its owner's identity owns. Treat such a key as a full-access credential.
- **A bucket created directly over S3** belongs to the identity of the key's owner and carries no project label, so neither the console nor this provider lists it at first. The platform assigns such a bucket to the **default project of the key's owner** periodically, within about an hour of its creation. If that is not the project Terraform is configured for, set `project_tag` to the owner's default project to see the bucket — the key the provider uses must belong to a member of that project (the organization's root user is normally a member of every user's default project); `prodata_s3_buckets` for any other project does not return it.

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
