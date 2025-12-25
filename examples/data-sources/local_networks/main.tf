data "prodata_local_networks" "all" {}

output "networks" {
  value = data.prodata_local_networks.all.local_networks
}
