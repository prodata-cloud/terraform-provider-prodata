# List the selectable Kubernetes versions and the latest stable one.
data "prodata_kubernetes_versions" "available" {}

# Use the latest stable version when creating a cluster.
output "latest_version" {
  value = data.prodata_kubernetes_versions.available.latest_version
}

output "all_versions" {
  value = [for v in data.prodata_kubernetes_versions.available.versions : v.version]
}

# Include internal debug builds in the list (latest_version never considers them).
data "prodata_kubernetes_versions" "with_debug" {
  include_debug = true
}
