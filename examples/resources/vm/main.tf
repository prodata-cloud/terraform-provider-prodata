resource "prodata_vm" "example" {
  name             = "my-vm"
  image_id         = 123
  cpu_cores        = 2
  ram              = 4
  disk_size        = 50
  disk_type        = "SSD"
  local_network_id = 456
  password         = "SecurePassword123"
}

# Cloud-init bootstrap. user_data is write-only (never stored in state); change
# user_data_hash to re-run cloud-init, which replaces the VM. Requires Terraform >= 1.11.
locals {
  user_data = <<-EOT
    #cloud-config
    package_update: true
    packages:
      - htop
  EOT
}

resource "prodata_vm" "bootstrapped" {
  name             = "bootstrapped-vm"
  image_id         = 123
  cpu_cores        = 2
  ram              = 4
  disk_size        = 50
  disk_type        = "SSD"
  local_network_id = 456
  password         = "SecurePassword123"

  user_data      = local.user_data
  user_data_hash = sha256(local.user_data)

  # The 30m create default covers the in-guest cloud-init run, including
  # slower Windows guests.
  timeouts {
    create = "30m"
  }
}
