data "prodata_public_ips" "all" {}

output "public_ips" {
  value = data.prodata_public_ips.all.public_ips
}
