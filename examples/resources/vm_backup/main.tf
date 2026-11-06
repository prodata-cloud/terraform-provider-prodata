resource "prodata_vm_backup" "daily_point" {
  vm_id    = prodata_vm.example.id
  name     = "before-maintenance"
  timeouts = { create = "90m" }
}
