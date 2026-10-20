# Changelog

All notable changes to this provider are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.29.0] - Unreleased

### Added

- **`prodata_local_network_attachment`**: a new resource that attaches a VM to one more ProData local
  network, as an additional network interface (a second NIC), and detaches it when destroyed. Until now
  this could only be done in the console. `vm_id` and `network_id` are required; `ip` is optional and
  validated as an IPv4 address at plan time (without it the platform assigns the next free address of the
  network, and it is recorded in `ip`); `region` and `project_tag` work as in the neighboring resources.
  `interface_id` is the ID of the interface itself, not of the network: it is what a detach and an import
  use. `mac_address` is the MAC address of the interface. A VM has one interface per network, so the
  network the VM was created in cannot be attached again; the VM's main interface is not managed by the
  resource and is never detached, and `prodata_vm.private_ip` and `local_network_id` go on describing it.
  Changing `vm_id`, `network_id`, `ip`, `region` or `project_tag` replaces the attachment;
  `reboot_after_attach` and `timeouts` can be changed in place, where the change is only recorded.
- `prodata_local_network_attachment`: **`reboot_after_attach`** (optional boolean, no default: unset is not the
  same as `false`), with the semantics of the argument of the same name of `prodata_public_ip_attachment`.
  The platform does not restart a running VM when an interface is added, and whether the guest picks the
  interface and its address up without a restart depends on the VM. With `reboot_after_attach = true` the
  provider restarts a running VM during `apply` — the platform's stop, then its start — and the create
  finishes only once the VM is `RUNNING` again; a VM that is not running is left alone. **The platform's stop
  is not a clean shutdown of the guest operating system**, so do not use the flag for a VM that needs one (a
  database, an NFS server). If the argument is not set and the VM is running, `apply` ends with a warning
  that the interface may work only after a restart; `false` silences it. If the restart fails, the
  attachment is kept in the state and marked as tainted.
- `prodata_local_network_attachment`: a **warning** when the VM has no cloud-init, because nothing then sets
  the address up inside the guest: it names the address and the MAC address of the interface.
- `prodata_local_network_attachment`: **`timeouts`** with `create` (default `15m`), a ceiling for the whole
  create, and `delete` (default `10m`), the longest a detach waits for a VM that is neither `RUNNING` nor
  `STOPPED`. A VM that is starting, stopping or rebooting is waited for and the call repeated; a call
  refused because another operation on the VM is in progress (code 961) is repeated at growing intervals for
  up to about two minutes, and then fails with the platform's answer. A VM that the
  platform shows as `STOPPED` but that is paused or hibernated is refused all the same (code 959): after
  three refusals in a row the provider stops and says so, instead of asking until the timeout. The VM is
  never stopped or restarted by a destroy.
- `prodata_local_network_attachment`: a refresh removes the attachment from the state only when the platform
  answers that the VM (633) or the interface (703) does not exist, or the interface now belongs to another
  network; any other error, a plain HTTP 404 page included, is reported and the state is kept. A destroy
  treats those two answers as "already gone" in the same way. An address changed outside Terraform is
  reported as a warning and leaves the state and the plan as they are, so an explicit `ip` does not turn
  into a replacement of a live interface; the warning says how to keep the address of the panel (`terraform
  state rm`, then `terraform import`; setting `ip` alone would plan a replacement) or to get the configured
  one back (`-replace`).
- `prodata_local_network_attachment`: an apply that was cut short after the attach request had gone out (an
  interruption, the `create` timeout, a lost connection) may have created an interface that Terraform did
  not record. The provider does not repeat such an attach. The error, of the attach that got no answer or of
  the next apply, which the platform refuses with 955, names the interface the VM has in that network and
  the `terraform import` command that takes it over.
- `prodata_local_network_attachment`: import with `vm_id:interface_id`. A primary interface cannot be imported.
- A detach refused with the generic "platform error" (627) is explained in the error, with the causes in the
  order to try them: a passing failure of the platform or of Proxmox (run `terraform destroy` again, at no
  cost to the VM); a removal that Proxmox has queued, because the VM does not hot-unplug network devices or
  the guest did not release the device, which takes effect only after the VM has been stopped and started
  again (a reboot from inside the guest is not enough); an interface that was only waiting for a restart to
  appear (stop and start once more; only if it was attached without `reboot_after_attach` to a VM that does
  not hot-plug network devices, and the VM has not been restarted since). Only if it still fails after that
  is it a question for support, and not for the console. The error also warns against `terraform state rm` in the meantime.
- `prodata_local_network_attachment`: an attach refused with 955, or one that got no answer, for a network in
  which the VM has only a primary interface does not offer to import it as it is: the resource does not
  manage a primary interface, and an import refuses one. The platform treats as primary both the interface in
  the network the VM was created in and any interface that carries a default gateway, and the list does not say
  which of the two an interface is. The VM's own interface usually carries a gateway too, so the message then
  names both possibilities: if the network is the VM's own, choose another one; if it is not, and the
  interface does not need to carry the gateway, move the gateway to the interface that is to carry the VM's
  default route, and import the interface then. Only an interface with no gateway on it is known to be the
  VM's own, and the message says so. The same advice comes with an import of such an interface, with a detach
  refused as primary (958), and with the warning about an address changed outside Terraform on an interface
  that has become primary (editing an address in the console can give an interface the gateway of the VM).
  It warns that the console's No gateway clears the gateway of every interface of the VM, not only of the one it
  is chosen on, and the documentation has the steps. On a primary interface that lists no gateway the warning
  about a changed address gives no such advice, because moving a gateway would change nothing for it: it says
  to ask support why the platform treats the interface as primary. When old data holds two interfaces of the
  VM in one network, the one that is not primary is the one named, and of two primary ones the one that
  carries a gateway.
- `prodata_local_network_attachment`: a `create` or `delete` timeout that runs out while another operation
  holds the VM (code 961) says so, and names that answer; a timeout that runs out while the VM is neither
  `RUNNING` nor `STOPPED` (code 959) says that. A paused or hibernated VM is recognized after three
  refusals in a row even if the status endpoint fails now and then.
- `docs/resources/local_network_attachment.md`: what the platform allows, how to configure the guest, when to
  restart the VM, in what order to destroy the attachments and the network, what a "platform error" on a
  detach means, how to get out of a drifted address, and what to do after an apply that was cut short.

## [0.28.1] - 2026-10-16

### Changed

- Documentation of `prodata_s3_bucket`, the S3 data sources and the provider index: corrected how S3
  access works. The old text said that S3 needs a separate key pair from **Account** → **S3 Credentials**
  (the console has no such page) and that the key used by Terraform is not an S3 key. A key generated
  with an `S3` permission is an S3 key as well, and it acts as its **owner**, not as the project: the
  page now explains why a key owned by another user of the organization is accepted by the S3
  endpoint but lists none of the project's buckets and gets `403` on them, which key to use or how to
  grant access, that over S3 a key's `READ`/`WRITE` level is not enforced, and why a bucket created
  directly over S3 shows up only in the default project of the key's owner, within about an hour.

## [0.28.0] - 2026-10-12

### Added

- `prodata_public_ip_attachment`: **`reboot_after_attach`** (optional boolean, no default: unset is
  not the same as `false`). The platform applies the network configuration of a public IP only
  when the VM starts, so on a running VM the address did not work until the VM was stopped and
  started again, while `apply` had already reported the attachment as created and everything that
  depends on it had gone on. With `reboot_after_attach = true` the provider restarts a running VM
  during `apply` — the platform's stop, then its start — and the create finishes only once the VM
  is `RUNNING` again, so whatever depends on the attachment is created after the restart. A VM
  that is not running is left alone (it applies the configuration at its next start), and one
  that is still starting is waited for first; one that went down between the attach and the
  restart is left stopped, with a warning. **The platform's stop is not a clean shutdown of the
  guest operating system** — it may power the VM off at once — so do not use the flag for a VM
  that needs one (a database, an NFS server): shut that one down from inside the VM and restart
  it yourself. `RUNNING` is the platform's view of the VM, and the provider cannot check that the
  address answers. A stop or a start that fails in a way that does not say whether the platform
  carried it out (its generic error, a gateway error, a dropped connection) is judged by what the
  VM does next and repeated for up to about three minutes, and a VM that reads `STOPPED` again
  after an accepted start is given as long; a clear refusal, such as "forbidden", ends the
  restart at once, and the `create` timeout bounds all of it. If the restart fails, the attachment
  is kept in the state and marked as tainted, so the next apply detaches the public IP and attaches
  it again; a VM that the failed restart left stopped stays stopped, so start it yourself.
- `prodata_public_ip_attachment`: a **warning** when the attachment is created on a VM that is
  running or starting and `reboot_after_attach` is not set, saying that the address works only
  after a restart. Setting `reboot_after_attach = false` says that you restart the VM yourself and
  silences it.
- `prodata_public_ip_attachment`: **`timeouts`** with `create` (default `15m`), a ceiling for the
  whole create: the attach call and, with `reboot_after_attach`, the restart. The provider returns
  as soon as it is done and does not wait out the timeout.

### Changed

- `prodata_public_ip_attachment`: an in-place update is no longer an error. `reboot_after_attach`
  and `timeouts` can be changed on an existing attachment; the change is only recorded — nothing is
  sent to the platform and the VM is not touched. `vm_id`, `public_ip_id`, `region` and
  `project_tag` still force a new resource.
- Documentation of `prodata_public_ip_attachment`: the advice to stop and start the VM after
  *detaching* a public IP is gone. The platform takes the local network's default gateway off the
  VM when a public IP is attached and does not currently put it back when the public IP is
  detached, so a VM that has applied the attachment can be left without a default route after the
  detach, at once or at its next restart, and a restart would not bring the gateway back. The
  provider never restarts the VM when the attachment is destroyed. The note in `prodata_vm` about attaching
  a public IP now says why the VM has to be restarted and points to `reboot_after_attach`.

### Notes

- Existing configurations plan with no changes and need no state upgrade: the new attributes are
  null in existing state. The only difference is the warning above when an attachment is created
  on a running VM. Setting `reboot_after_attach` on an existing attachment is a one-time in-place
  update that restarts nothing, and so is importing one.

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
