# Contributing

Thanks for contributing to the ProData Terraform provider.

## Prerequisites

- Go >= 1.25 (see `go.mod`)
- Terraform >= 1.0 (only needed for the docs format step and acceptance tests)

## Development workflow

```sh
go build ./...     # compile
make test          # unit + client tests (no credentials, no network)
make lint          # golangci-lint (config in .golangci.yml)
gofmt -l .         # must print nothing
make generate      # terraform fmt examples + validate docs against the schema
```

All of the above run in CI on every pull request; please make sure they pass locally first.

## Documentation

The docs under `docs/` are **hand-authored** (curated prose, multiple examples, custom
sections) and validated against the provider schema — they are *not* generated. Do **not**
run `tfplugindocs generate`; it would overwrite the curated content. When you add or change
a schema attribute, update the corresponding page in `docs/` by hand and run `make generate`,
which runs `tfplugindocs validate` to catch drift.

Keep `CHANGELOG.md` up to date (Keep a Changelog format) and add an entry under the relevant
heading for any user-visible change.

## Acceptance tests

Acceptance tests create real infrastructure and require `PRODATA_*` credentials; they run
only with `TF_ACC=1`. See `README.md` for the required environment variables. Mutating tests
against a production host are blocked unless `PRODATA_ACC_ALLOW_PROD_MUTATION=1` is set, and
every test resource carries the disposable `tfacc-` name prefix so `make sweep` can reap
leftovers.

## Pull requests

- Keep changes focused and explain the motivation in the description.
- Do not change the published provider schema (resource/attribute names and types) or the
  backend API contract without a clear migration note — they are user-facing.
- Never include credentials, secrets, or internal infrastructure details in commits, code,
  or documentation.
