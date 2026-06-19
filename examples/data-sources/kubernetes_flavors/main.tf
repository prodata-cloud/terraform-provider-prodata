# List the master-node flavors (configurations) available in the region.
# Omit high_availability to get both HA and single-master flavors.
data "prodata_kubernetes_flavors" "all" {}

# Restrict to highly-available flavors only.
data "prodata_kubernetes_flavors" "ha" {
  high_availability = true
}

# Use the first HA flavor as a cluster's master_flavor_id.
output "ha_flavor_id" {
  value = data.prodata_kubernetes_flavors.ha.flavors[0].id
}

output "flavors" {
  value = [for f in data.prodata_kubernetes_flavors.all.flavors : {
    id        = f.id
    vcpu      = f.vcpu
    ram       = f.ram
    disk_size = f.disk_size
    ha        = f.high_availability
  }]
}
