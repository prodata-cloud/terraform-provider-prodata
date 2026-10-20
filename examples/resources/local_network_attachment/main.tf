resource "prodata_local_network_attachment" "example" {
  vm_id      = prodata_vm.example.id
  network_id = prodata_local_network.storage.id

  # Optional: without it the platform assigns the next free address of the network.
  ip = "192.168.30.10"
}
