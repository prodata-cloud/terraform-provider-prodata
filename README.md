# Terraform Provider ProData

Terraform provider for [ProData Cloud](https://registry.terraform.io/providers/prodata-cloud/prodata),
built on the [Terraform Plugin Framework](https://developer.hashicorp.com/terraform/plugin/framework).

Full documentation and examples for every resource and data source are published on
the [Terraform Registry](https://registry.terraform.io/providers/prodata-cloud/prodata/latest/docs).

## Supported resources and data sources

**Resources**

- `prodata_vm` — virtual machine (with optional cloud-init `user_data`)
- `prodata_volume` / `prodata_volume_attachment` — block volumes and their attachment to a VM
- `prodata_public_ip` / `prodata_public_ip_attachment` — public IPs and their attachment to a VM
- `prodata_local_network` — local (private) network
- `prodata_s3_bucket` — S3-compatible object-storage bucket
- `prodata_lb` — L4 (TCP/UDP) load balancer
- `prodata_kubernetes_cluster` / `prodata_kubernetes_node_pool` — Managed Kubernetes

**Data sources**

- `prodata_image` / `prodata_images`
- `prodata_vm` / `prodata_vms`
- `prodata_volume` / `prodata_volumes`
- `prodata_public_ip` / `prodata_public_ips`
- `prodata_local_network` / `prodata_local_networks`
- `prodata_s3_bucket` / `prodata_s3_buckets`
- `prodata_lb` / `prodata_lbs`
- `prodata_kubernetes_cluster` / `prodata_kubernetes_node_pool`
- `prodata_kubernetes_versions` / `prodata_kubernetes_flavors`

## Requirements

- [Go](https://go.dev/dl/) >= 1.25
- [Terraform](https://developer.hashicorp.com/terraform/downloads) >= 1.0 (only needed to run acceptance tests)

## Building

```sh
make install
# or
go build ./...
```

## Testing

The suite follows the three layers used by HashiCorp-maintained providers.

### Unit and client tests (no credentials, no network)

Hermetic tests of our own logic: the validators and helpers we wrote, import-id
parsers, and the HTTP client's request/response encoding and error mapping (via
`httptest`). No configuration required:

```sh
make test
# or
go test ./...
```

### Acceptance tests (`TF_ACC`)

Acceptance tests drive the full resource lifecycle (create, read, update, delete),
an import round-trip, and plan stability through the real Terraform runtime. As
described in HashiCorp's
[acceptance testing guidance](https://developer.hashicorp.com/terraform/plugin/testing/acceptance-tests),
they **create real infrastructure that may cost money**, so they run only when
`TF_ACC` is set. Without it — for example in CI without secrets — they are skipped.

Set the provider credentials and scope, then run:

```sh
export PRODATA_API_BASE_URL=https://<host>   # host only; the provider appends the API path
export PRODATA_API_KEY_ID=<key-id>
export PRODATA_API_SECRET_KEY=<secret>
export PRODATA_REGION=<region>
export PRODATA_PROJECT_TAG=<project-tag>

make testacc
```

The `prodata_lb` acceptance test additionally needs a pre-existing network and a
backend VM in it (provisioning them inline would be slow and couple the test to
other resources):

```sh
export PRODATA_LB_TEST_NET_ID=<network-id>
export PRODATA_LB_TEST_VM_GUID=<vm-guid>
```

Acceptance tests create resources with a disposable `tfacc-` name prefix. Mutating
runs against a production host are blocked unless you explicitly opt in:

```sh
export PRODATA_ACC_ALLOW_PROD_MUTATION=1
```

### Sweepers

Sweepers delete acceptance resources left behind by an interrupted run, matching
the `tfacc-` prefix. They use the same `PRODATA_*` credentials:

```sh
make sweep
```
