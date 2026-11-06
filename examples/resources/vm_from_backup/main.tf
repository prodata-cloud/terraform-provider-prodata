resource "prodata_vm_from_backup" "recovery" {
  backup_id   = prodata_vm_backup.daily_point.id
  name        = "recovered-server"
  cpu_cores   = 2
  ram         = 16
  description = "Recovery copy"
  timeouts    = { create = "90m" }
}
