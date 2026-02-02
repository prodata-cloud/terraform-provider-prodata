resource "prodata_volume_attachment" "example" {
  vm_id     = prodata_vm.example.id
  volume_id = prodata_volume.example.id
}
