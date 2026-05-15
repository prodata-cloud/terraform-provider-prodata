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
