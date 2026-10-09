# Changelog

All notable changes to this provider are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.27.0] - 2026-10-09

### Added

- **Ephemeral resource `prodata_kubernetes_kubeconfig`** (Terraform 1.10 or later): reads a
  cluster's connection details — `host`, `cluster_ca_certificate`, `client_certificate`,
  `client_key`, `token` and the full `raw_config` — for the `kubernetes` and `helm` providers
  **without writing them to Terraform state or to a saved plan**; they live in memory for the run
  only. The attributes have the names and encodings of `kube_config` on the cluster resource and
  data source, so moving a configuration over is a change of reference. The lookup fails with an
  error, rather than returning empty values, when the cluster is not found, is deleted, has no
  kubeconfig yet, or its kubeconfig names no API server or holds neither a client certificate
  with its key nor a token: a `kubernetes` or `helm` provider that is given no host or no
  credentials may fall back to other connection settings it finds (a kubeconfig named by
  `config_path` or `KUBE_CONFIG_PATH`, or the service account of the pod it runs in) and connect
  to whatever cluster those name. None of this applies while `cluster_id` is unknown, that is while
  the cluster is created or replaced in the same run: the provider is then configured with unknown
  values and may treat them as missing, so keep the cluster and its workloads in separate
  configurations. The failure also cannot help when a kubeconfig file is configured
  as well: the provider applies the values you pass on top of that file and takes whatever you
  leave unset, such as a token or an `exec` plugin, from it. Do not set `config_path`,
  `config_paths`, `KUBE_CONFIG_PATH` or `KUBE_CONFIG_PATHS` for a provider configured from this
  resource.
- `prodata_kubernetes_cluster` (resource and data source): **`exclude_credentials_from_state`**
  (optional boolean, no default: unset behaves as `false`). When `true`, `kube_config` and
  `private_key_encoded` are always null and are never stored in state — a cluster's kubeconfig is
  a cluster-admin credential that Kubernetes cannot revoke, and `private_key_encoded` is the SSH
  private key of the nodes. Set it on the resource and on every data source that reads the
  cluster, and read the kubeconfig with the new ephemeral resource. Set `public_key` when you
  create the cluster as well: a key pair the platform generates (`ssh_access_enabled = true`
  without `public_key`) can be returned only as `private_key_encoded`, so with the opt-out on you
  would never receive its private half; `terraform plan` warns about this combination when a
  cluster is created. The SSH key cannot be changed on an existing cluster (a `public_key` added to
  one that was created without it is accepted but never sent to the platform).

### Fixed

- `prodata_kubernetes_cluster`: **an in-place update that does not change `kubernetes_version` no
  longer waits for — or refuses — the cluster.** That is an update of only `timeouts` or
  `exclude_credentials_from_state`, or one that sets a write-once input (`network_id`, `public_key`,
  `ssh_access_enabled`) after an import. Every in-place update used to take the per-cluster lock and
  require a modifiable cluster: it failed for a cluster in the `FAIL` state and waited, up to the
  update timeout, while an operation was in flight on the cluster, although the change calls nothing
  on it. Only a `kubernetes_version` upgrade does that now.

### Notes

- Nothing changes unless you set the flag (apart from the fix above): it is off by default,
  adding it needs no state upgrade, and existing configurations plan with no changes. Writing
  `exclude_credentials_from_state = false` explicitly on an existing cluster is a one-time in-place
  update that changes nothing on the cluster. As for any in-place update of the cluster, that plan
  shows `kube_config` as known after apply, so leave the argument unset (or `null`) unless you are
  turning the exclusion on.
- Turning the flag on removes the credentials from the state written from then on. State versions
  written before (a remote backend's history, a local `terraform.tfstate.backup`) still contain
  them, and so does the state of a cluster you import until the first apply with the flag set.
  For a cluster whose SSH key pair the platform generated, that also removes the only copy of the
  private key that Terraform holds from the state: copy it out first if you need SSH access.
- Other sensitive values are not covered by this change: for example `prodata_vm` `password` is
  still stored in state (see the provider page).
- There is no ephemeral counterpart for the nodes' SSH private key; bring your own `public_key`.

## [0.26.1] - 2026-10-08

### Fixed

- `prodata_kubernetes_cluster`: **`control_plane_size` and `master_flavor_id` can be set from a
  variable, a data source or another resource's attribute again.** Since 0.23.0 the "set exactly
  one of `master_flavor_id` / `control_plane_size`" check treated a value Terraform does not know
  yet as "not set". Terraform validates the configuration with variables and data sources still
  unknown before it plans, so any configuration that did not write the value as a literal —
  `control_plane_size = var.size`, or the `master_flavor_id =
  data.prodata_kubernetes_flavors.….flavors[0].id` form shown in the documentation — was rejected
  with `a control-plane size is required`, even when the variable was set (`terraform plan -var
  size=small`). The check now waits until both values are known; Terraform validates again with
  the real values during plan and apply, so a configuration that ends up with both or neither is
  still rejected.
- `prodata_kubernetes_node_pool`: **`autoscaling` can be set as a whole value — from an object
  variable or a conditional.** `autoscaling = var.autoscaling`, or `autoscaling = var.autoscale ?
  { min_nodes = 1, max_nodes = 3 } : null`, failed in `terraform validate` and `terraform plan`
  with `Value Conversion Error … Received unknown value, however the target type cannot handle
  unknown values`: Terraform validates with variables still unknown, and the check could not read
  an `autoscaling` block that is unknown as a whole. The "`node_count` or `autoscaling`, not both
  and not neither" check and the bounds check now wait until the block is known. When it is known
  only after apply, a clash with `node_count` is reported during apply, before the pool is
  created, rather than at plan; on an update plan, `status` and a `node_count` that is not written
  in the configuration show as `(known after apply)` meanwhile.
- `prodata_lb`: **`backend_group` and `port` can be set as a whole value that is known only after
  apply** (for example a conditional on another resource's attribute); the plan no longer fails
  with the same `Value Conversion Error`. On an existing load balancer, a switch of the backend
  mode or `node_pool_id` that only shows up during apply still stops that apply with `Provider
  produced inconsistent final plan`; run it again (see Known Limitations on the `prodata_lb`
  page). The rule that `description` is not configurable on a node pool (CCM) load balancer now
  also applies at plan time when `node_pool_id` is the id of a pool created in the same apply;
  before, it was reported only during apply, after the pool had already been created.

## [0.26.0] - 2026-10-07

### Changed

- `prodata_kubernetes_cluster`: **`node_ip_range` validation is stricter.** The old check was a
  single regular expression that accepted `999.1.1.1-0.0.0.0`, a reversed range, a range in some
  other network, and a range containing the network's gateway; the panel accepted all of these and
  broke later. Now:
  - at validate time the value must be an IPv4 `start-end` (no spaces, no IPv6, no leading zeros)
    with `start` strictly below `end`;
  - at plan time the range is checked against the local network of `network_id`: it must lie
    inside the network's CIDR and must not contain the network's gateway, otherwise the plan
    fails. A range that includes the network or broadcast address only warns. The check needs the
    network to be readable in the cluster's region and project; if it is not, it is skipped with a
    warning. When `network_id` is not known at plan time the check runs during apply, before the
    cluster is created;
  - omitting `node_ip_range` now produces a warning on create: the automatic range is sized from
    the capacity known at creation time and is never widened, so node pools added later can run
    out of addresses;
  - an in-place update or a plain destroy of an **existing cluster is not re-checked** while
    `node_ip_range` and `network_id` stay unchanged, so a cluster whose range already contains the
    gateway can still be updated and destroyed;
  - a **replacement** (changed `name`, `pod_cidr`, flavor, region, `-replace`, …) creates a new
    cluster and is checked like a create. For a replacement the plan shows only the errors;
    warnings appear during apply. If such a change is pending, `terraform destroy` runs the same
    check — use `terraform destroy -refresh=false` or revert the change.

  Upgrade note: a malformed or reversed `node_ip_range` (octet above 255, leading zeros, `start`
  above or equal to `end`) now fails validation on every plan and destroy, even for an
  already-created cluster. To keep such a cluster, remove `node_ip_range` from the configuration —
  the stored value stays in state and nothing is replaced; editing the value forces a new cluster.
  A range that contains the gateway fails the plan of a new or replaced cluster only.

## [0.25.0] - 2026-10-07

### Added

- `prodata_local_network` data source: look a network up by `name` as well as by `id`
  (exactly one is required). `id` is now `Optional`+`Computed` instead of `Required`, so existing
  configs keep working. The name is matched exactly (case-sensitive) among the networks of the
  selected region and project; no match, or more than one network with the same name (the panel
  does not enforce unique names on every path), is an error that points to the `id` lookup.
- `prodata_kubernetes_cluster`: recognize the backend's new `DELETING` lifecycle status — a
  lingering state while a cluster's asynchronous teardown runs. `terraform destroy` now polls
  through `DELETING` until the cluster reads `DELETED`; the `status` attribute can report
  `DELETING`; and `terraform plan`/`refresh` keeps a `DELETING` cluster in state instead of
  dropping it as gone.

### Changed

- `prodata_kubernetes_cluster`: the default **delete timeout is raised from 5m to 45m** —
  cluster teardown is asynchronous and the backend finalizer can take 30-45 minutes, so the
  old timeout gave up before the real terminal verdict. If teardown fails (or times out
  server-side) the cluster is left in `FAIL` — which now **holds the cluster name** until the
  failed cluster is deleted — and `terraform destroy` surfaces a clear error and keeps the
  resource in state instead of reporting success. Creating a cluster whose name is still held
  by a same-named `DELETING` or `FAILED` cluster now fails with tailored guidance (wait for
  teardown, or delete the failed cluster) rather than a generic "already exists".

### Fixed

- `prodata_lb`: panel code 662 — a `network_id` that is not a local network of your account
  (an unknown id, a deleted network, or a public IP's id) — is now reported as a clear "Local
  network not found" message instead of the raw API error. The panel returns 662 for these
  once the matching `panel-main` change is deployed; until then an unknown network gets the
  misleading code 737 (not enough free IPs in the network) and a public IP's id the generic
  code 627.
- `prodata_lb`: load-balancer calls now ask the panel for English (`X-Lang: en`, as the
  Kubernetes calls already do), so an error the provider does not map yet is shown in English
  rather than in the language of the API key user's profile. This needs a panel that lets
  `X-Lang` take precedence over the profile's language; on an older panel a language set on the
  profile still wins.

## [0.24.0] - 2026-08-28

### Removed

- **BREAKING:** `prodata_kubernetes_cluster.default_node_pool` is removed. A cluster is now
  **control-plane-only**; every worker pool — including the first — is a standalone
  `prodata_kubernetes_node_pool` resource. This makes a pool's CPU/RAM/disk change replace
  only that pool (never the cluster), allows any number of independently-managed pools, and
  makes a zero-pool cluster a valid steady state.

### Changed

- `prodata_kubernetes_cluster`: creating a cluster no longer provisions a worker pool. Add
  worker capacity with one or more `prodata_kubernetes_node_pool` resources.
- `prodata_kubernetes_node_pool`: deleting a cluster's last worker pool is now allowed
  (control-plane-only is legal). Against a backend not yet upgraded the panel still returns
  code 756; the provider surfaces it as a clear message.
### Migration

- Existing clusters migrate **without destroying worker nodes**: remove the `default_node_pool`
  block, add a `prodata_kubernetes_node_pool`, and **`terraform import`** the existing pool (its
  id is the cluster's lowest-id worker pool) before any apply — a clean `terraform plan` (no
  changes) is the success gate. Autoscaling pools: declare `autoscaling` and omit `node_count`.
  Full recipe in the `prodata_kubernetes_cluster` resource docs.

> **Deploy ordering:** this release requires the matching control-plane-only `panel-main` **and**
> `panel-k8s` changes in the target region. Deploy/promote the backend first; against an
> un-upgraded backend the old create path NPEs on a null `nodePoolName`. (Mirrors the 0.23.0
> `node_ip_range` backend-first precedent.) The published release is held until the backend is
> promoted `test → main` for uz + kz.

## [0.23.0] - 2026-06-24

### Added

- `prodata_kubernetes_cluster`: new `control_plane_size` attribute (`small` / `medium` /
  `large`) as a convenience alias for `master_flavor_id`. The provider resolves it to the
  right master flavor based on `high_availability`, by ranking the region's master-flavor
  catalog by capacity (smallest → `small`). Set **exactly one** of `control_plane_size` or
  `master_flavor_id`.
- `prodata_kubernetes_flavors` data source: each flavor now exports its derived `size`
  (`small`/`medium`/`large`), i.e. the value to pass to `control_plane_size`.

### Changed

- `prodata_kubernetes_cluster`: `master_flavor_id` is now **Optional+Computed** (was
  Required) — supply it explicitly, or set `control_plane_size` and let the provider resolve
  and export it. Exactly one of the two is required.
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

[Unreleased]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.24.0...HEAD
[0.24.0]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.23.0...v0.24.0
[0.23.0]: https://github.com/prodata-cloud/terraform-provider-prodata/compare/v0.22.0...v0.23.0
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
