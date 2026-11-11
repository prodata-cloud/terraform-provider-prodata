# Ordinary optional guest address configuration. The image must satisfy the
# static Netplan and root QEMU guest-agent requirements in the resource docs.
resource "prodata_local_network_attachment" "example" {
  vm_id               = prodata_vm.example.id
  network_id          = prodata_local_network.storage.id
  ip                  = "192.0.2.10"
  configure_guest     = true
  reboot_after_attach = true
}

# Claiming the network's declared gateway address is a separate, explicit option.
# The network must belong to this project and the address must be unreserved.
# This creates no router or default route.
resource "prodata_local_network_attachment" "gateway_address" {
  vm_id                 = prodata_vm.gateway.id
  network_id            = prodata_local_network.client.id
  ip                    = "198.51.100.1"
  allow_gateway_address = true
}
