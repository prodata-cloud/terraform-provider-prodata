data "prodata_local_network" "example" {
  id = 12345
}

output "network_cidr" {
  value = data.prodata_local_network.example.cidr
}
