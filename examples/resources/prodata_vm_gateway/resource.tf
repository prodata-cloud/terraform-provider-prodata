# A VM has a single IPv4 default route. Use only one gateway resource per VM.
resource "prodata_vm_gateway" "local" {
  vm_id      = prodata_vm.example.id
  network_id = prodata_local_network_attachment.example.network_id
  gateway    = "10.20.0.1"
}
