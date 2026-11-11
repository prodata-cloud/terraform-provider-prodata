resource "prodata_vm_snapshot" "before_change" {
  vm_id       = prodata_vm.example.id
  name        = "before_change"
  include_ram = false
}
