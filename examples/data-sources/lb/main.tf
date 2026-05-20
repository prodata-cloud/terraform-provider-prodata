data "prodata_lb" "web" {
  id = 42
}

output "lb_public_ip" {
  value = data.prodata_lb.web.public_ip
}

output "lb_private_ip" {
  value = data.prodata_lb.web.private_ip
}

output "lb_backend_vms" {
  value = data.prodata_lb.web.vm_ids
}

output "lb_ports" {
  value = data.prodata_lb.web.port
}
