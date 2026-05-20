data "prodata_lbs" "all" {}

output "lb_count" {
  value = length(data.prodata_lbs.all.load_balancers)
}

output "lb_names" {
  value = [for lb in data.prodata_lbs.all.load_balancers : lb.name]
}

output "external_lb_public_ips" {
  value = [
    for lb in data.prodata_lbs.all.load_balancers :
    lb.public_ip
    if lb.type == "external" && lb.public_ip != null
  ]
}
