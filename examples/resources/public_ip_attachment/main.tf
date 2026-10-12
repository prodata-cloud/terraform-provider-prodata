resource "prodata_public_ip_attachment" "example" {
  vm_id        = prodata_vm.example.id
  public_ip_id = prodata_public_ip.example.id

  # Power-cycle the VM when the address is attached, so that the address works when apply
  # finishes. The platform's stop is not a clean shutdown, and it happens again whenever
  # this attachment is replaced: set it only for a VM that can take that.
  reboot_after_attach = true
}
