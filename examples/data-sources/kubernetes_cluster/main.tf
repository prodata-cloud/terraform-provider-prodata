# Look up a cluster by name (only live clusters are visible by name)...
data "prodata_kubernetes_cluster" "by_name" {
  name = "prod-cluster"
}

# ...or by id.
data "prodata_kubernetes_cluster" "by_id" {
  id = 42
}

output "cluster_status" {
  value = data.prodata_kubernetes_cluster.by_name.status
}

output "cluster_api_endpoint" {
  value = data.prodata_kubernetes_cluster.by_name.api_endpoint
}

# Wire the kubernetes provider from the looked-up cluster's kube_config. Gate
# downstream consumers on status — kube_config is null until the kubeconfig is
# available (usually at or shortly after SUCCESS).
provider "kubernetes" {
  host                   = data.prodata_kubernetes_cluster.by_name.kube_config.host
  cluster_ca_certificate = base64decode(data.prodata_kubernetes_cluster.by_name.kube_config.cluster_ca_certificate)
  client_certificate     = base64decode(data.prodata_kubernetes_cluster.by_name.kube_config.client_certificate)
  client_key             = base64decode(data.prodata_kubernetes_cluster.by_name.kube_config.client_key)
}
