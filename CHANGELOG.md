# Changelog

All notable changes to this provider are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.23.0] - 2026-06-24

### Changed

- `prodata_kubernetes_cluster`: `node_ip_range` is now **Optional+Computed**. When omitted,
  the platform auto-allocates a free contiguous range from `network_id` (sized for the
  cluster's master and worker capacity) and reports it back in state; when set, the value is
  used as-is. It is no longer write-once — the API now echoes it, so it is read back on Read
  and `terraform import` (no need to re-supply it after import). An explicit change still
  forces a new resource. Range validation is retained for user-supplied values.
- `prodata_kubernetes_cluster` data source now exports `node_ip_range`.

### Removed

- **Breaking:** `prodata_kubernetes_cluster.node_subnet` has been removed. The node subnet
  prefix is derived server-side from the local network's own mask, so the input was never
  authoritative for addressing. Remove `node_subnet` from your configurations; existing
  state drops it automatically on the next refresh.

> **Deploy ordering:** this release depends on the matching `panel-main` change (server-side
> node-IP-range allocation + `nodeIpRange` exposed on the cluster API). Deploy the backend
> first; otherwise omitting `node_ip_range` has nothing to allocate the range.

## [0.22.0] - 2026-06-21

### Added

- Plan-time validators: `prodata_vm` (`cpu_cores` >= 1, `ram` >= 1, `disk_size` >= 10, and
  `name` 3-63 chars / letters-digits-hyphens / at least one letter) and
  `prodata_kubernetes_cluster` (`node_subnet` 1-32, `node_ip_range` as an IPv4 `start-end`
  range). Invalid input now fails at plan; the bounds match what the backend enforces.
- `prodata_image` data source now populates both `name` and `slug` from the API (the
  lookup key you did not supply is no longer null); both are Optional+Computed.
- Optional client-side request pacing via the `PRODATA_MAX_RPS` environment variable
  (off by default) to pre-empt server-side 429s on large applies.
- CI workflow (build, vet, gofmt, golangci-lint, unit tests) on PRs and the default
  branch, plus Dependabot for Go modules and GitHub Actions.

### Fixed

- `prodata_volume`: detect out-of-band deletion (the by-id endpoint returns soft-deleted
  volumes; Read now confirms via the list).
- `prodata_volume_attachment`: resolve the attachment by its VmDisk id (was using a volume
  id), fixing spurious state removal and re-attach.
- `prodata_local_network`: refuse to adopt a name-conflicting network with a mismatched
  cidr/gateway (was a destroy/recreate loop); serialize create/delete to remove the
  parallel-create 627 race (no more `-parallelism=1` workaround).
- `prodata_s3_bucket`: reconcile acl/versioning (and error on an `object_lock_enabled`
  mismatch) when adopting an existing bucket; clearer "bucket not empty" message on destroy.
- `prodata_lb`: keep imported pre-source load balancers updatable; the `prodata_lb` and
  `prodata_lbs` data sources no longer return soft-deleted balancers.
- `prodata_public_ip_attachment`: confirm the managed IP is still attached before the
  VM-scoped detach.
- `prodata_vm`: read back via the status endpoint on update (covers ERROR-status VMs),
  keep the planned name on rename, settle cpu/ram/disk before create returns (removes a
  spurious post-create diff), and surface an orphaned VM on a name-conflict recovery failure.
- `prodata_kubernetes_cluster`: preflight `master_flavor_id` and give an actionable message
  on a backend provisioning failure.
- Client: redact raw response bodies from error diagnostics (avoid leaking secrets); retry
  idempotent (GET) requests on transient transport errors; remove the 60s client-level
  timeout so per-resource `timeouts` apply.

### Changed

- Bump `terraform-plugin-framework` to v1.19.0.

## [0.21.0] - 2026-06-20

### Added

- **Managed Kubernetes.** New resources and data sources for ProData Managed Kubernetes:
  - `prodata_kubernetes_cluster` — manages a cluster and its inline `default_node_pool`.
    Supports in-place Kubernetes version upgrades, fixed-size or autoscaling worker pools, and a
    structured, sensitive `kube_config` block (`host`, `cluster_ca_certificate`,
    `client_certificate`, `client_key`, `token`, `raw_config`) for wiring the `kubernetes`
    and `helm` providers directly.
  - `prodata_kubernetes_node_pool` — manages additional worker pools on a cluster, with
    in-place scaling and autoscaling on/off/bounds transitions.
  - `prodata_kubernetes_cluster` and `prodata_kubernetes_node_pool` data sources — look up
    a cluster or pool by `id` or `name`.
  - `prodata_kubernetes_versions` data source — the selectable Kubernetes versions and the
    latest stable one (`latest_version`).
  - `prodata_kubernetes_flavors` data source — the master-node flavors available for a
    cluster's `master_flavor_id`.

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

## [0.18.2] - 2026-06-04

### Documentation

- `prodata_lb`: document the round-robin balancing behavior and clarify that
  `backend_group.vm_ids` takes VM **guids** (the `prodata_vm.guid` attribute),
  not numeric ids.

## [0.18.1] - 2026-06-02

### Fixed

- Client: stop retrying API error **627** (the panel's generic "unhandled error"
  HTTP 500 catch-all). 627 is not a transient/busy condition, so retrying it only
  hung `terraform apply` for the full timeout and masked the real cause; it now
  surfaces immediately.

## [0.18.0] - 2026-05-21

### Added

- `prodata_vm`: a computed **`guid`** attribute — the VM's stable global
  identifier. Use it wherever another resource references a VM by guid (for
  example a load balancer's `backend_group.vm_ids`).

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

[Unreleased]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.22.0...HEAD
[0.22.0]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.21.0...v0.22.0
[0.21.0]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.20.0...v0.21.0
[0.20.0]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.19.0...v0.20.0
[0.19.0]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.18.2...v0.19.0
[0.18.2]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.18.1...v0.18.2
[0.18.1]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.18.0...v0.18.1
[0.18.0]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.17.1...v0.18.0
[0.17.1]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.17.0...v0.17.1
[0.17.0]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.16.0...v0.17.0
[0.16.0]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.15.0...v0.16.0
[0.15.0]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.14.0...v0.15.0
[0.14.0]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.13.0...v0.14.0
[0.13.0]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.11.40...v0.12.0
