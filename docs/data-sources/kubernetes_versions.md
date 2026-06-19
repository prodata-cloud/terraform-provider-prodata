---
page_title: "prodata_kubernetes_versions Data Source - ProData Provider"
subcategory: "Kubernetes"
description: |-
  List the selectable Kubernetes versions and the latest stable one.
---

# prodata_kubernetes_versions (Data Source)

List the selectable Kubernetes versions and the latest stable one.

~> **Note:** The backend returns versions account-wide rather than per-region, so a version listed here is not guaranteed to be offered in every region; cluster create validates the version against the target region and errors if it is unavailable.

## Example Usage

```terraform
data "prodata_kubernetes_versions" "available" {}

resource "prodata_kubernetes_cluster" "main" {
  kubernetes_version = data.prodata_kubernetes_versions.available.latest_version
  # ...
}

output "all_versions" {
  value = [for v in data.prodata_kubernetes_versions.available.versions : v.version]
}
```

## Schema

### Optional

- `region` (String) Region ID override. If omitted, uses the provider's default region.
- `project_tag` (String) Project tag override. If omitted, uses the provider default.
- `include_debug` (Boolean) Include internal debug builds in `versions`. Defaults to `false`. `latest_version` never considers debug builds.

### Attribute Reference

- `latest_version` (String) The highest non-debug version, by numeric `major.minor.patch` order. Null if no stable version is available.
- `versions` (List of Object) Available versions, in the order returned by the server. Each entry has:
  - `id` (Number) Version ID.
  - `version` (String) Version string (e.g. `v1.31.4`).
  - `is_debug` (Boolean) Whether this is an internal debug build.
