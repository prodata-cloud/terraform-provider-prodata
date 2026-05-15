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
