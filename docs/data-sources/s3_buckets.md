---
page_title: "prodata_s3_buckets Data Source - ProData Provider"
description: |-
  List all ProData S3 (Ceph RGW) buckets owned by the project.
---

# prodata_s3_buckets (Data Source)

List all ProData S3 (Ceph RGW) buckets owned by the project resolved from `project_tag` (or the provider default). Pagination is handled internally — the provider follows the server's `continuationToken` until the listing is exhausted.

~> **Note:** Per-bucket `versioning` and `object_lock_enabled` are **not** fetched by this data source. Doing so would require an additional round-trip per bucket per refresh — wasteful for inventory-style queries. Use [`prodata_s3_bucket`](./s3_bucket.md) for the buckets where you need those fields.

## Example Usage

```terraform
data "prodata_s3_buckets" "all" {}

output "bucket_names" {
  value = data.prodata_s3_buckets.all.names
}

output "total_buckets" {
  value = length(data.prodata_s3_buckets.all.buckets)
}

output "total_size_bytes" {
  value = sum([for b in data.prodata_s3_buckets.all.buckets : b.size])
}
```

## Schema

### Optional

- `region` (String) Region ID override. If not specified, uses the provider's default region.
- `project_tag` (String) Project tag override. If not specified, uses the provider's default project tag.

### Attribute Reference

- `names` (List of String) Convenience list of just the bucket names, in the order returned by the server.
- `buckets` (List of Object) List of buckets owned by the project. Each bucket has the following attributes:
  - `name` (String) The bucket name.
  - `creation_date` (String) Server-reported bucket creation timestamp (RFC3339).
  - `size` (Number) Total size in bytes of all objects in the bucket.
  - `object_count` (Number) Number of objects currently stored in the bucket.

## Known Limitations

- **No per-bucket versioning / object lock.** See note above — use the singular data source for those fields.
- **No filtering or sorting parameters.** The list is server-ordered and returned in full. Filter on the Terraform side via `for` expressions if needed.
- **`size` and `object_count` are point-in-time.** Same caveat as the singular data source — these values reflect the server's accounting at read time.
