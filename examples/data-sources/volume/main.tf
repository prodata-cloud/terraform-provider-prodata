data "prodata_volume" "example" {
  id = 12345
}

output "volume_name" {
  value = data.prodata_volume.example.name
}
