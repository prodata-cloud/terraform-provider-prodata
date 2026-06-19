# Pick the latest stable Kubernetes version and an HA master flavor.
data "prodata_kubernetes_versions" "stable" {}

data "prodata_kubernetes_flavors" "ha" {
  high_availability = true
}

data "prodata_kubernetes_flavors" "standard" {
  high_availability = false
}

# A fixed-size cluster with a highly-available control plane.
resource "prodata_kubernetes_cluster" "main" {
  name               = "prod-cluster"
  kubernetes_version = data.prodata_kubernetes_versions.stable.latest_version
  network_id         = prodata_local_network.k8s.id
  pod_cidr           = "10.244.0.0/16"
  node_subnet        = 24
  node_ip_range      = "10.0.0.10-10.0.0.20"
  high_availability  = true
  master_flavor_id   = data.prodata_kubernetes_flavors.ha.flavors[0].id

  default_node_pool = {
    name       = "workers"
    vcpu       = 4
    ram        = 8
    disk_size  = 80
    node_count = 3
  }
}

# An autoscaling cluster with a public API endpoint and SSH access to the nodes.
resource "prodata_kubernetes_cluster" "edge" {
  name               = "edge-cluster"
  kubernetes_version = data.prodata_kubernetes_versions.stable.latest_version
  network_id         = prodata_local_network.k8s.id
  pod_cidr           = "10.245.0.0/16"
  node_subnet        = 24
  node_ip_range      = "10.0.1.10-10.0.1.20"
  master_flavor_id   = data.prodata_kubernetes_flavors.standard.flavors[0].id

  public_endpoint_enabled = true
  ssh_access_enabled      = true
  public_key              = file(pathexpand("~/.ssh/id_ed25519.pub"))

  default_node_pool = {
    name      = "workers"
    vcpu      = 2
    ram       = 4
    disk_size = 40

    autoscaling = {
      min_nodes = 1
      max_nodes = 5
    }
  }
}

# Wire the kubernetes provider directly from the structured kube_config block.
# The certificate fields are base64 as they appear in the kubeconfig, so wrap
# them in base64decode().
provider "kubernetes" {
  host                   = prodata_kubernetes_cluster.main.kube_config.host
  cluster_ca_certificate = base64decode(prodata_kubernetes_cluster.main.kube_config.cluster_ca_certificate)
  client_certificate     = base64decode(prodata_kubernetes_cluster.main.kube_config.client_certificate)
  client_key             = base64decode(prodata_kubernetes_cluster.main.kube_config.client_key)
}
