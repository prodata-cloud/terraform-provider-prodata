# An additional fixed-size worker pool on an existing cluster.
resource "prodata_kubernetes_node_pool" "gpu" {
  cluster_id = prodata_kubernetes_cluster.main.id
  name       = "gpu-workers"
  vcpu       = 8
  ram        = 32
  disk_size  = 120
  node_count = 2
}

# An autoscaling worker pool. Omit node_count when autoscaling is set — the
# autoscaler owns the count, exported as the computed node_count attribute.
resource "prodata_kubernetes_node_pool" "batch" {
  cluster_id = prodata_kubernetes_cluster.main.id
  name       = "batch-workers"
  vcpu       = 4
  ram        = 8
  disk_size  = 80

  autoscaling = {
    min_nodes = 1
    max_nodes = 10
  }
}
