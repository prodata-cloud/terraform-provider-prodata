data "prodata_image" "ubuntu" {
  slug = "ubuntu-22.04"
}

output "image_id" {
  value = data.prodata_image.ubuntu.id
}
