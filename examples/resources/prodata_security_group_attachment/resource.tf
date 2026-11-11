resource "prodata_security_group_attachment" "example" {
  security_group_id = prodata_security_group.web.id
  vm_id             = prodata_vm.example.id
  interface_id      = prodata_local_network_attachment.private.interface_id
}
