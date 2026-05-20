# Changelog

All notable changes to this provider are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `prodata_lb` schema: `name` length (3-63) and charset (letters/digits/hyphens,
  no leading or trailing hyphen) plan-time validators.
- `prodata_lb` schema: `network_id` minimum-value validator.
- `LbProtocolTCP`/`LbProtocolUDP` exported client constants.
- `subcategory: "Load Balancer"` front-matter on the LB resource and data
  source docs so the Terraform Registry sidebar groups them correctly.
- Regression test asserting the LB client normalizes lowercase `protocol`
  values from the server to upper-case.

### Changed

- LB client: `protocol` is normalized to upper-case in
  `lbDTO.toLoadBalancer()`. Pre-existing load balancers that the server stored
  as `"tcp"`/`"udp"` no longer trigger spurious destroy+recreate plans against
  the `OneOf("TCP","UDP")` schema validator.
- LB resource Update wraps `ConfigureLoadBalancerFrontend`/`CCM` in
  `RetryOnBusy` to match Create's handling of API error 627 (resource busy).
- `LoadBalancerRequest.Backends` is now tagged `omitempty`; CCM Update no
  longer emits `"backends":null` on the wire.
- `.goreleaser.yml` uses GoReleaser v2's `archives[].formats` plural form.

### Removed

- Vestigial `preserveNodePool` parameter on the resource's internal
  `applyServerState` (both branches were identical assignments).
- Unused "pure helper" validator shims (`validateLbType`,
  `validateLbProtocol`, `validatePortCount`, `validateBackendGroupExactlyOne`)
  — the framework validators in the schema are the production path and are
  already covered by direct unit tests.

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

[Unreleased]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.15.0...HEAD
[0.15.0]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.14.0...v0.15.0
[0.14.0]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.13.0...v0.14.0
[0.13.0]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.11.40...v0.12.0
