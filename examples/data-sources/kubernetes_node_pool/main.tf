# Look up a node pool within a cluster by name...
data "prodata_kubernetes_node_pool" "by_name" {
  cluster_id = 42
  name       = "gpu-workers"
}

# ...or by id.
data "prodata_kubernetes_node_pool" "by_id" {
  cluster_id = 42
  id         = 7
}

output "pool_node_count" {
  value = data.prodata_kubernetes_node_pool.by_name.node_count
}

# Autoscaling is exposed as flat computed attributes on the data source.
output "pool_autoscaling" {
  value = {
    enabled   = data.prodata_kubernetes_node_pool.by_name.autoscale_enabled
    min_nodes = data.prodata_kubernetes_node_pool.by_name.min_nodes
    max_nodes = data.prodata_kubernetes_node_pool.by_name.max_nodes
  }
}
