# Terraform Provider ProData

Terraform provider for [ProData Cloud](https://registry.terraform.io/providers/prodata-cloud/prodata),
built on the [Terraform Plugin Framework](https://developer.hashicorp.com/terraform/plugin/framework).

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
