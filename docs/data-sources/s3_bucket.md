---
page_title: "prodata_s3_bucket Data Source - ProData Provider"
subcategory: "Storage"
description: |-
  Look up a single ProData S3 (Ceph RGW) bucket by name.
---

# prodata_s3_bucket (Data Source)

Look up a single ProData S3 (Ceph RGW) bucket by name. The bucket must belong to the project resolved from `project_tag` (or the provider default); a bucket that exists in another project surfaces as a clear error, not a silent empty result.

~> **Note:** `acl` is intentionally not exposed by the data source. The S3 ACL API exposes grants, not canned ACLs, and the canned value cannot be reliably round-tripped from grants. Use the [`prodata_s3_bucket`](../resources/s3_bucket.md) resource if you need to manage ACLs.

## Example Usage

```terraform
data "prodata_s3_bucket" "example" {
  name = "my-bucket"
}

output "bucket_versioning" {
  value = data.prodata_s3_bucket.example.versioning
}

output "bucket_object_lock_enabled" {
  value = data.prodata_s3_bucket.example.object_lock_enabled
}

output "bucket_size_bytes" {
  value = data.prodata_s3_bucket.example.size
}

output "bucket_object_count" {
  value = data.prodata_s3_bucket.example.object_count
}
```

## Schema

### Required

- `name` (String) Bucket name to look up.

### Optional

- `region` (String) Region ID override. If not specified, uses the provider's default region.
- `project_tag` (String) Project tag override. If not specified, uses the provider's default project tag.

### Attribute Reference

- `id` (String) Data source identifier — equal to `name`.
- `creation_date` (String) Server-reported bucket creation timestamp (RFC3339).
- `versioning` (Boolean) `true` if object versioning is enabled. A suspended or never-configured bucket reads as `false`.
- `object_lock_enabled` (Boolean) `true` if S3 object lock is enabled on the bucket.
- `size` (Number) Total size in bytes of all objects currently stored in the bucket.
- `object_count` (Number) Number of objects currently stored in the bucket.

## Known Limitations

- **ACL is not exposed.** See note above — canned ACLs do not round-trip from S3 grants.
- **Cross-project lookups error.** Requesting a bucket whose underlying S3 tag points at a different project than `project_tag` returns an error, not an empty result. This is deliberate — silently returning nothing would let one project shadow another's bucket name in plan output.
- **`size` and `object_count` are point-in-time.** They reflect the server's accounting at read time and can lag the actual bucket state by a small amount on busy Ceph clusters.
