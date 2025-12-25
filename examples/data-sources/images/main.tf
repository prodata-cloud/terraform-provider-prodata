data "prodata_images" "all" {}

output "images" {
  value = data.prodata_images.all.images
}
