data "prodata_public_ip" "example" {
  id = 12345
}

output "ip_address" {
  value = data.prodata_public_ip.example.ip
}
