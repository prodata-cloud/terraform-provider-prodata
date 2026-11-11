resource "prodata_security_group" "web" {
  name = "web"

  rules = [
    {
      direction        = "in"
      action           = "ACCEPT"
      protocol         = "tcp"
      source           = "192.0.2.0/24"
      destination_port = "443"
    },
    { direction = "in", action = "DROP" },
    { direction = "out", action = "ACCEPT" }
  ]
}
