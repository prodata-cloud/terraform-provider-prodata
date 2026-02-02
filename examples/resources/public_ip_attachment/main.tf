resource "prodata_public_ip_attachment" "example" {
  vm_id        = prodata_vm.example.id
  public_ip_id = prodata_public_ip.example.id
}
