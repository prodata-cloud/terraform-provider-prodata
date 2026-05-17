resource "prodata_s3_bucket" "example" {
  name = "my-bucket"
}

resource "prodata_s3_bucket" "versioned" {
  name       = "my-versioned-bucket"
  acl        = "private"
  versioning = true
}

resource "prodata_s3_bucket" "locked" {
  name                = "my-locked-bucket"
  versioning          = true
  object_lock_enabled = true
}

resource "prodata_s3_bucket" "ephemeral" {
  name          = "my-ephemeral-bucket"
  force_destroy = true
}
