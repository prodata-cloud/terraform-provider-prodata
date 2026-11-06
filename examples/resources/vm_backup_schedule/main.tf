resource "prodata_vm_backup_schedule" "nightly" {
  vm_id = prodata_vm.example.id
  name  = "nightly"
  cron  = "0 0 2 * * *"
}
