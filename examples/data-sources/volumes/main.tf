data "prodata_volumes" "all" {}

output "volumes" {
  value = data.prodata_volumes.all.volumes
}
