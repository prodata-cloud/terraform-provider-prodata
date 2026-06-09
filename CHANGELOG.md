# Changelog

All notable changes to this provider are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.20.0] - 2026-06-09

### Removed

- **BREAKING:** `prodata_vm`: the `user_data_hash` argument is removed. The provider now
  computes the cloud-init payload hash itself (sha256, kept in the resource's private state)
  and detects changes automatically, so you no longer supply a hash. **Migration:** delete
  the `user_data_hash = ...` line from your configuration; keep `user_data`. After upgrading,
  the first `plan` is clean (the old hash is dropped from state); do not change `user_data`
  in the same step as the upgrade, as the baseline re-establishes on the next create/replace.

### Notes

- Because the hash now lives in private state (seeded only when Terraform creates the VM),
  an **imported** VM is not tracked for `user_data` changes until it is next replaced.

## [0.19.0] - 2026-06-05

### Added

- `prodata_vm`: `user_data` — cloud-init user data applied at first boot via a
  NoCloud ISO. It is **write-only** (the raw payload is never stored in state nor
  shown in a plan; requires Terraform >= 1.11) and is validated client-side
  (must start with `#cloud-config` or `#!`, max 64 KiB) so malformed payloads
  fail at plan time instead of round-tripping to the API.
- `prodata_vm`: `user_data_hash` — the plan-visible companion to the write-only
  `user_data`. Set it to a hash of the payload (e.g.
  `sha256(file("cloud-init.yaml"))`); changing it replaces the VM so cloud-init
  re-runs at first boot.
- `prodata_vm`: a `timeouts` block with a configurable `create` timeout.

### Changed

- `prodata_vm`: the create timeout now defaults to **30m** (was a hard-coded 5m).
  The provider polls for VM readiness while the backend waits for the in-guest
  cloud-init run (up to ~600s on Linux, ~1200s on Windows) plus a
  stop/detach-ISO/restart cycle. The 30m default covers the Windows worst case,
  which exceeded the old default.

### Notes

- A cloud-init failure inside the guest is not reported back by the API — a VM
  whose cloud-init failed still reports `RUNNING` — so a successful `apply` does
  not by itself prove the `user_data` script ran without errors.

## [0.17.1] - 2026-05-21

### Added

- `subcategory` front-matter on all remaining resources and data sources
  (`Compute`, `Storage`, `Networking`), so the Terraform Registry sidebar groups
  the entire provider — extending the Load Balancer grouping added in 0.16.0.

### Fixed

- Documented three `prodata_vm` attributes that were present in the schema but
  missing from the resource docs: `public_ip_id` (optional) and the read-only
  `image_name` and `image_slug`.
- `prodata_public_ips` data source: corrected the `project_tag` argument
  description, which incorrectly read "Project ID".

### Changed

- Provider example in the docs index now pins `version = "~> 0.17"`.
- Documentation is hand-maintained and checked with `tfplugindocs validate`
  (enforcing a `Compute`/`Storage`/`Networking`/`Load Balancer` subcategory
  allowlist) instead of the unused `generate` scaffold. Build tooling only.

## [0.17.0] - 2026-05-21

### Added

- Acceptance test suite (`TF_ACC`) for `prodata_lb` and `prodata_s3_bucket`,
  driving the full create/read/update/delete lifecycle, an import round-trip,
  and plan-stability checks through the real Terraform runtime.
- Test sweepers (`make sweep`) that delete leaked acceptance resources by their
  disposable `tfacc-` name prefix.
- Production-host mutation guard: mutating acceptance tests are skipped against
  a production host unless `PRODATA_ACC_ALLOW_PROD_MUTATION=1` is set.
- `README.md` documenting the build and the three test layers (unit/client,
  acceptance, and sweepers).

### Changed

- Reworked the unit and client tests onto shared helpers.
- Raised the `go` directive to 1.25.8 and added `terraform-plugin-testing` and
  `terraform-plugin-go` as direct test dependencies. Affects building from
  source and CI only; the released cross-compiled binaries are unaffected.

## [0.16.0] - 2026-05-21

### Added

- `prodata_lb` schema: `name` length (3-63) and charset (letters/digits/hyphens,
  no leading or trailing hyphen) plan-time validators.
- `prodata_lb` schema: `network_id` minimum-value validator.
- `LbProtocolTCP`/`LbProtocolUDP` exported client constants.
- `subcategory: "Load Balancer"` front-matter on the LB resource and data
  source docs so the Terraform Registry sidebar groups them correctly.
- `prodata_lb` import now also accepts the composite `{region}/{id}@{project_tag}`
  form for importing load balancers outside the provider's default scope; the
  bare-ID form continues to work.
- Regression test asserting the LB client normalizes lowercase `protocol`
  values from the server to upper-case; unit tests for import-ID parsing and
  the new error-humanizing helper.

### Changed

- LB client: `protocol` is normalized to upper-case in
  `lbDTO.toLoadBalancer()`. Pre-existing load balancers that the server stored
  as `"tcp"`/`"udp"` no longer trigger spurious destroy+recreate plans against
  the `OneOf("TCP","UDP")` schema validator.
- LB resource Update wraps `ConfigureLoadBalancerFrontend`/`CCM` in
  `RetryOnBusy` to match Create's handling of API error 627 (resource busy).
- `LoadBalancerRequest.Backends` is now tagged `omitempty`; CCM Update no
  longer emits `"backends":null` on the wire.
- A user-supplied `description` on a CCM (node pool) load balancer is now
  rejected at plan time on **update** as well as create — the panel owns the
  CCM description (`"CCM: <name>"`) and ignores caller values.
- LB diagnostics surface human-readable messages for known error codes
  (duplicate name, not found, insufficient free IPs, busy) instead of the raw
  `api error [code]` string.
- `.goreleaser.yml` uses GoReleaser v2's `archives[].formats` plural form.

### Removed

- Vestigial `preserveNodePool` parameter on the resource's internal
  `applyServerState` (both branches were identical assignments).
- Unused "pure helper" validator shims (`validateLbType`,
  `validateLbProtocol`, `validatePortCount`, `validateBackendGroupExactlyOne`)
  — the framework validators in the schema are the production path and are
  already covered by direct unit tests.

### Internal

- New `internal/tfutil.StringOrNull` helper centralizes the "empty server
  string → null state value" idiom; adopted by the LB resource and data
  sources.
- Renamed the LB-only `doV1` client helper to `doLBV1`; promoted the
  per-call terminal-status set constructors to package-level vars.

## [0.15.0] - 2026-05-20

### Added

- `prodata_lb` resource for L4 load balancers (TCP/UDP). Supports both
  VM-backed (`backend_group.vm_ids`, `FRONTEND` source) and Kubernetes
  node-pool-backed (`backend_group.node_pool_id`, `CCM` source) balancers,
  with mode-switch protection via `RequiresReplace`.
- `prodata_lb` data source — look up a single load balancer by ID.
- `prodata_lbs` data source — list load balancers visible to the project.
- `terraform-plugin-framework-timeouts` v0.7.0 promoted to a direct
  dependency to back the resource's `timeouts` block.

## [0.14.0] - 2026-05-19

### Added

- Transparent retry of HTTP 429 (rate-limited) responses with exponential
  backoff and `Retry-After` header support. Survives bulk applies through
  edge-layer rate limits (e.g. Cloudflare error 1015).

## [0.13.0] - 2026-05-18

### Removed

- **BREAKING:** `force_destroy` attribute on `prodata_s3_bucket`. Bucket
  destroy now only succeeds when the bucket is empty; objects must be
  removed explicitly before `terraform destroy`.

## [0.12.0] - 2026-05-17

### Changed

- **BREAKING:** `prodata_s3_bucket.versioning` is now a `bool` (previously a
  three-state string: `"enabled"`/`"suspended"`/`"disabled"`). Migrate
  `"enabled"` → `true`, `"suspended"`/`"disabled"` → `false`.

## [0.11.40] and earlier

See the [git tag history](https://github.com/prodata-cloud/terraform-provider-prodata/tags)
for release-by-release commits. Notable in the 0.11 line: addition of
`prodata_s3_bucket` resource and data sources, the `prodata_public_ip_attachment`
restart note, plus VM and volume CRUD improvements.

[Unreleased]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.20.0...HEAD
[0.20.0]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.19.0...v0.20.0
[0.15.0]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.14.0...v0.15.0
[0.14.0]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.13.0...v0.14.0
[0.13.0]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.11.40...v0.12.0
