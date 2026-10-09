# Look up an existing cluster. exclude_credentials_from_state keeps its credentials out of
# the state this data source writes.
data "prodata_kubernetes_cluster" "main" {
  name                           = "prod-cluster"
  exclude_credentials_from_state = true
}

# The cluster's connection details, held in memory for this Terraform run only: never
# written to the state or to a saved plan. Requires Terraform 1.10 or later.
ephemeral "prodata_kubernetes_kubeconfig" "main" {
  cluster_id = data.prodata_kubernetes_cluster.main.id
}

# The certificate fields are base64 as they appear in the kubeconfig, so wrap them in
# base64decode(). Configure the provider from these values alone: do not also set
# config_path (or KUBE_CONFIG_PATH), which the provider would read and mix in.
provider "kubernetes" {
  host                   = ephemeral.prodata_kubernetes_kubeconfig.main.host
  cluster_ca_certificate = base64decode(ephemeral.prodata_kubernetes_kubeconfig.main.cluster_ca_certificate)
  client_certificate     = base64decode(ephemeral.prodata_kubernetes_kubeconfig.main.client_certificate)
  client_key             = base64decode(ephemeral.prodata_kubernetes_kubeconfig.main.client_key)
}
