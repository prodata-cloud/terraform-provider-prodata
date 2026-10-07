data "prodata_local_network" "example" {
  name = "my-network"
}

# Or look it up by ID:
# data "prodata_local_network" "example" {
#   id = 12345
# }

output "network_id" {
  value = data.prodata_local_network.example.id
}

output "network_cidr" {
  value = data.prodata_local_network.example.cidr
}
