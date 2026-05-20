resource "prodata_lb" "web" {
  name        = "web-lb"
  description = "Production web tier"
  type        = "external"
  protocol    = "TCP"
  network_id  = prodata_local_network.web.id

  port = [
    { port = 443, target_port = 8443 },
    { port = 80, target_port = 8080 },
  ]

  backend_group = {
    vm_ids = [
      prodata_vm.web_1.guid,
      prodata_vm.web_2.guid,
    ]
  }
}

resource "prodata_lb" "metrics" {
  name       = "metrics-collector"
  type       = "internal"
  protocol   = "UDP"
  network_id = prodata_local_network.metrics.id

  port = [
    { port = 8125, target_port = 8125 },
  ]

  backend_group = {
    vm_ids = [prodata_vm.collector.guid]
  }
}

resource "prodata_lb" "ingress" {
  name       = "ingress-lb"
  type       = "external"
  protocol   = "TCP"
  network_id = data.prodata_local_network.k8s.id

  port = [
    { port = 443, target_port = 30443 },
  ]

  backend_group = {
    node_pool_id = 42
  }

  timeouts = {
    create = "30m"
    update = "30m"
    delete = "15m"
  }
}
