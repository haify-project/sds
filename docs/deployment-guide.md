# SDS — Installation & Deployment Guide

From raw storage nodes to a working DRBD-backed cluster with gateways, HA,
Kubernetes CSI, the AI Copilot, and optional WAN replication.

The per-node package list, with the symptom each missing piece produces, is
[`node-prerequisites.md`](./node-prerequisites.md). Day-to-day operation is
covered in [`user-guide.md`](./user-guide.md).

Reference clusters run **Ubuntu 24.04** (amd64 and arm64). Package names below
are Ubuntu's; adjust for other distros.

---

## 0. Architecture

```
sds / web UI / sds-mcp / sds-ai
        │  gRPC 3374 · REST 3375 · UI 3376 · metrics 9433
        ▼
  sds-controller ──(dispatch over SSH)──►  storage nodes
        │                                   ├─ DRBD 9 (kernel) + drbd-utils
        └─ BBolt DB /var/lib/sds/sds.db     ├─ drbd-reactor (promoters, HA)
                                            ├─ LVM / ZFS pools
                                            └─ OCF agents (gateways)
```

- **Control plane:** one `sds-controller` process with a BBolt database
  (`[database] path`, default `/var/lib/sds/sds.db`). It drives every node over
  SSH through the `dispatch` library; there is no per-node SDS agent. Commands
  aimed at the controller's own address run locally without SSH.
- **Listeners:** gRPC on `[server] port` (default `3374`); the REST gateway on
  `3375` (fixed, bound to `[server] listen_address`); the web UI on `[ui] port`
  (default `3376`); Prometheus metrics on `[metrics] port` (default `9433`). The
  UI proxies `/v1/` to the REST gateway and `/ai/` to the AI Copilot on
  `127.0.0.1:7634`, so a browser needs only the UI port.
- **Data plane:** DRBD 9 replicates block volumes; `drbd-reactor` promoters fail
  over mounts, VIPs and services; gateways (NFS / iSCSI / NVMe-oF) export a
  resource behind a floating service IP.
- **Optional:** Kubernetes CSI driver, `sds-ai` Copilot, controller **Self-HA**
  (the controller floats on a DRBD-backed VIP), WAN replication (DRBD over a
  per-resource `sds-proxy` pair), encrypted replication (kernel TLS), off-cluster
  backups (rclone).

Without Self-HA the controller can run on a storage node or on a separate host.
Self-HA requires it to run on a registered node.

---

## 1. Build the binaries

Requires Go 1.26 (`go.mod` pins toolchain `go1.26.8`) and Node.js for the web UI
(CI uses Node 22).

```bash
git clone <sds-repo> && cd sds
(cd web-ui && npm ci)
make build
```

`make build` builds the web UI, copies it into `ui/dist` (embedded into the
controller via `go:embed`), and produces `bin/sds-controller`, `bin/sds`,
`bin/sds-mcp`, `bin/service-ip` (always for Linux), `bin/csi-controller` and
`bin/csi-node` — all except `service-ip` for the build host's OS.

For Linux nodes built on another OS or architecture, build the UI once and
cross-compile:

```bash
make ui-sync
for c in controller:sds-controller cli:sds mcp:sds-mcp service-ip:service-ip; do
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/${c#*:} ./cmd/${c%%:*}
done
# arm64 nodes: GOARCH=arm64
```

A controller built without `make ui-sync` serves a placeholder page instead of
the UI; the API and `sds` are unaffected.

Other binaries:
- `sds-ai` (`cmd/sds-ai`, its own Go module): `cd cmd/sds-ai && go build .`
- `sds-proxy` (WAN transport): built from the separate `sds-proxy` repository.
- CSI image: `Dockerfile.csi` (section 9).

---

## 2. Prepare the storage nodes

On **every** node. Details and failure symptoms: `node-prerequisites.md`.

1. **DRBD 9 kernel module, `drbd-utils`, `drbd-reactor`** (LINBIT packages or
   source builds):
   ```bash
   cat /proc/drbd            # version: 9.x
   drbdadm --version
   drbd-reactor --version
   ```
   Keep drbd-utils and drbd-reactor versions matched on all nodes. Reactor parses
   `drbdsetup status --json`; a drbd-utils too old for it produces JSON reactor
   cannot parse, it logs `IGNORING resource … expected ','` and never fails over
   (seen with drbd-utils 9.31 + reactor 1.11; drbd-utils 9.34 fixed it).
   drbd-reactor must be running (`systemctl enable --now drbd-reactor`);
   `ha self enable` refuses otherwise.

2. **LVM** (`lvm2`) for LVM pools; `thin-provisioning-tools` for incremental
   backups (`thin_delta`); `zfsutils-linux` only for ZFS pools.

3. **OCF resource agents** for gateways:
   ```bash
   apt-get install -y resource-agents-extra   # Filesystem, nfsserver, exportfs, nvmet-*
   ```
   `resource-agents-base` alone lacks `Filesystem`, which every gateway promoter
   starts with. `ha create` and Self-HA use systemd mount units and
   `service-ip@.service` instead of OCF agents.

4. Per gateway type, on the nodes of the exported resource:
   - iSCSI: `apt-get install -y targetcli-fb python3-rtslib-fb`
   - NFS: `apt-get install -y nfs-kernel-server`
   - NVMe-oF: `apt-get install -y nvme-cli linux-modules-extra-$(uname -r)`
     (Ubuntu cloud kernels ship `nvmet-tcp` only in `linux-modules-extra`).
     Gateway creation loads `nvmet` and `nvmet-tcp` (or `nvmet-rdma` for
     `--transport rdma`) and adds them to `/etc/modules-load.d/nvmet.conf`.

5. Optional features:
   - backups: `rclone` on every diskful node of a backed-up resource
   - encrypted replication: `ktls-utils` (tlshd), `openssl`, DRBD ≥ 9.2 with TLS
   - encryption at rest (`resource create --encrypt`): `cryptsetup`, `dm-crypt`

6. **A data disk** per diskful node: a raw block device (`/dev/sdb`, `/dev/vdb`)
   with no filesystem or mount on it.

`sds health-check` (after section 5) reports per node whether DRBD,
drbd-reactor (installed and running) and the resource agents are present.

---

## 3. SSH trust and dispatch config

The controller reaches nodes over SSH. Node commands use `sudo`, so the SSH user
must be root or have passwordless sudo; Self-HA additionally requires
passwordless **root** SSH between every pair of nodes.

On the controller node:

```bash
sudo test -f /root/.ssh/id_ed25519 || \
  sudo ssh-keygen -t ed25519 -N "" -f /root/.ssh/id_ed25519 -C root@controller
sudo cat /root/.ssh/id_ed25519.pub
# append that key to /root/.ssh/authorized_keys on EVERY node, including this one
```

`/root/.dispatch/config.toml`:

```toml
[ssh]
user = "root"
port = 22
key_path = "/root/.ssh/id_ed25519"
strict_host_key = false
timeout = "30s"

# One section per node, keyed by the node's IP address.
[hosts."<node1-ip>"]
addresses = ["<node1-ip>"]
user = "root"
key_path = "/root/.ssh/id_ed25519"

[hosts."<node2-ip>"]
addresses = ["<node2-ip>"]
user = "root"
key_path = "/root/.ssh/id_ed25519"

[hosts."<node3-ip>"]
addresses = ["<node3-ip>"]
user = "root"
key_path = "/root/.ssh/id_ed25519"
```

- **Key `[hosts.*]` by IP address.** The controller asks dispatch for hosts by
  address. A section named after a node (`[hosts.node1]`) never matches, and
  settings then fall through to `~/.ssh/config`: any `Host` entry whose
  `HostName` is that IP supplies its user, port and key, which commonly yields
  `unable to authenticate, attempted methods [none]`. dispatch resolves host
  section > group section > `~/.ssh/config` > `[ssh]` defaults, so `user` and
  `key_path` set inside the IP-keyed section always win.
- **Point the controller at this file** with `[dispatch] config_path`. Unset, it
  uses `~/.dispatch/config.toml` of the user the controller runs as. A path that
  does not exist stops the controller at startup.
- **Host keys.** dispatch records host keys in `known_hosts` (default
  `~/.ssh/known_hosts`, i.e. `/root/.ssh/known_hosts`); `strict_host_key = false`
  adds unknown hosts automatically. A key that *changed* — a rebuilt node, or a
  VM that regenerates host keys — is rejected with `host key changed: <ip>`
  regardless of that setting. Remove the stale entry with
  `ssh-keygen -R <ip> -f /root/.ssh/known_hosts`; dispatch rereads the file when
  it changes, so no restart is needed.

---

## 4. Install and start the controller

```bash
sudo install -d /opt/sds/bin /etc/sds
sudo install -m 755 bin/sds-controller bin/service-ip /opt/sds/bin/
sudo install -m 755 bin/service-ip /usr/local/bin/service-ip
sudo install -m 755 bin/sds /usr/local/bin/sds
sudo ln -sf sds /usr/local/bin/sds-cli      # older scripts call it sds-cli
sudo cp configs/sds-controller.service configs/service-ip@.service /etc/systemd/system/
```

Minimal `/etc/sds/controller.toml`:

```toml
[server]
listen_address = "0.0.0.0"
port = 3374

[dispatch]
config_path = "/root/.dispatch/config.toml"
parallel = 10

[log]
level = "info"
format = "json"

[storage]
default_pool_type = "thin_pool"
default_snapshot_suffix = "_snap"
```

`configs/controller.toml.example` describes `[database]`, `[wan]`, `[tls]`
(gRPC/REST/UI transport), `[metrics]`, `[ui]` and `[alert]`. It sets
`[metrics] port = 9090` and `[ui] port = 8080`, not the defaults (9433, 3376);
change or drop those lines if you start from it. API authentication is
`[auth] enabled` + `token` (at least 16 characters) or `[rbac]` with per-user
tokens. Other sections with their defaults:
`[resource] auto_tiebreaker = true`, `fault_domain_label = "host"`;
`[gateway] auto_state_volume = true`, `state_volume_size_gb = 1`;
`[schedule] enabled = true`; `[audit] enabled = true`;
`[storage] verify_schedule = "0 3 1 * *"`; `[self_ha] extra_services = []`.

`configs/sds-controller.service` runs
`/opt/sds/bin/sds-controller --config /etc/sds/controller.toml` as root with
`Environment="HOME=/root"`. Keep the `HOME` line: systemd sets no `$HOME` for a
system unit without it, and dispatch's default lookups (`~/.dispatch`,
`~/.ssh/known_hosts`, default keys) then miss root's files; remote operations
fail with `unable to authenticate, attempted methods [none]` while operations
on the controller's own node still work.

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now sds-controller
ss -tlnp | grep -E ':(3374|3375|3376|9433)\b'
```

`sds` talks to `127.0.0.1:3374` by default; use `--controller <host>:3374`
(or `-c`) from elsewhere, plus `--token` / `--tls*` when `[auth]` / `[tls]` are
enabled.

---

## 5. Register nodes and create pools

```bash
sds node register --name node1 --address <node1-ip>
sds node register --name node2 --address <node2-ip>
sds node register --name node3 --address <node3-ip>
sds node list
```

`--name` is a label of your choosing. Registration also records the node's real
hostname, which generated `.res` files use for their `on <host>` sections.
`--replication-address` puts DRBD traffic on a separate NIC or subnet.

A node that must never be picked as an automatic diskless quorum tiebreaker —
typically a WAN/DR node whose public address is not configured on its own
interface — needs:

```bash
sds node label <dr-node> sds.tiebreaker=false
```

Without it a 2-node LAN resource can pull the DR node into its mesh and fail
`drbdadm up` with `IP <addr> not found on this host`.

Create a pool on each diskful node's data disk. The name gets an `sds_` prefix
(`vg0` → VG `sds_vg0`); without `--type` the controller's
`storage.default_pool_type` applies (thin pool by default):

```bash
sds pool create --name vg0 --nodes node1,node2,node3 --devices /dev/sdb
sds pool list
```

`--type lvm` builds a thick VG, `--type zfs` a zpool. A diskless tiebreaker node
needs no pool.

---

## 6. Create a replicated resource

```bash
sds resource create --name data --port 7000 --nodes node1,node2 --size 10G --pool vg0
sds resource status data
```

`--port` and `--size` are required. Omit `--nodes` to let the controller place
`--replicas` (default 2) copies by free pool space. New resources get
`quorum majority` + `on-no-quorum io-error`; with `[resource] auto_tiebreaker`
(default on) a 2-node resource gains a diskless tiebreaker on a third registered
node, so one node loss keeps quorum. The controller also force-promotes once on
create so the fresh resource has an UpToDate copy, and installs
`sds-drbd-up.service` on its nodes so resources come back after a reboot.

Filesystem and mount (volume id is positional):

```bash
sds resource fs    data 0 ext4      --node node1
sds resource mount data 0 /mnt/data --node node1
```

---

## 7. Gateways

A gateway is a DRBD resource plus a drbd-reactor promoter that mounts it, brings
up a floating service IP (CIDR) and starts the export on whichever node holds
Primary. Give each gateway its own resource; do not also mount it by hand or put
it under `ha create`.

```bash
# NFS
sds gateway nfs create --resource share --service-ip 192.168.1.200/24 --export-path /share

# iSCSI (LIO)
sds gateway iscsi create --resource lun1 \
    --iqn iqn.2026-01.com.example:sds.lun1 --service-ip 192.168.1.100/24

# NVMe-oF (TCP, port 4420)
sds gateway nvme create --resource ns1 \
    --nqn nqn.2026-01.com.example:sds.ns1 --service-ip 192.168.1.150/24
```

Creation checks the needed OCF agents, plus `rpc.nfsd`/`exportfs` for NFS and
`targetcli` for iSCSI, on the resource's diskful nodes before writing anything, adds the cluster-private state volume
when the resource has only one volume (`[gateway] auto_state_volume`), formats
volumes that carry no filesystem, writes `/etc/drbd-reactor.d/sds-{nfs,iscsi,nvmeof}-<resource>.toml`
and reloads drbd-reactor. iSCSI runs on LIO only: `--implementation tgt` or
`iet` is refused.

Check with `sds gateway status --resource <r>` and, on the Primary,
`drbd-reactorctl status` and `ss -tlnp`.

---

## 8. High availability

### Per-resource HA

```bash
sds ha create data --mount /mnt/data --fstype ext4 [--vip 192.168.1.210/24] [--services myapp.service]
```

drbd-reactor promotes the resource, mounts it, raises the VIP and starts the
services on a surviving node when the Primary fails. With `--vip`, the
`service-ip` helper and `service-ip@.service` are installed on nodes that lack
them (from the controller's own build; a node of another architecture is
refused).

### Controller Self-HA

Puts the controller database on a DRBD resource `sds-meta` mounted at
`/var/lib/sds`, and makes drbd-reactor run the controller and its VIP on the
Primary.

Before enabling:
- the controller runs on a registered node, with `/etc/sds/controller.toml` and
  `/etc/systemd/system/sds-controller.service` in place; the unit's `ExecStart`
  names the binary by absolute path;
- every other node has the controller's architecture, or a build for its
  architecture sits beside the running binary as `sds-controller-<goarch>`
  (e.g. `/opt/sds/bin/sds-controller-arm64`); otherwise `enable` refuses that
  node before changing anything;
- passwordless root SSH works between every pair of target nodes, and the
  dispatch key named in the dispatch config exists on each of them;
- drbd-reactor is active on all of them, and `sds-controller` is not running on
  any node but this one.

```bash
sds ha self enable --pool vg0 --vip 192.168.1.250/24 [--nodes a,b,c] [--port 7999] [--size 1]
sds -c 192.168.1.250:3374 ha self status
sds ha evict sds-meta          # move the controller to another node
```

`enable` copies the running controller binary (or the node's `-<goarch>` build)
to the path the unit's `ExecStart` names on the other nodes, along with
`controller.toml`, the dispatch config and the unit itself, disables `sds-controller` autostart on the standbys, and hands over to
drbd-reactor; the command's own connection drops during the handoff. Use the VIP
for everything afterwards (`<vip>:3374`, `http://<vip>:3376/`).

To ship a new controller build: install it at the unit's `ExecStart` path on
**every** node (`mv` the running file aside first; overwriting it in place fails
with `Text file busy`), then `sds ha evict sds-meta` to restart it on another
node. Restarting `sds-controller` on the active node also restarts the promoter
target and fails over just the same.

`[self_ha] extra_services = ["sds-ai.service"]` makes extra units follow the
controller; it is read when `enable` writes the promoter config.

---

## 9. Kubernetes CSI driver (optional)

The Kubernetes nodes must be the DRBD storage nodes (the node plugin promotes
and mounts DRBD on the host). The controller runs outside Kubernetes and is
reached at an IP or VIP on port 3374.

1. Build the image for the node architecture and import it on every node:
   ```bash
   docker build -f Dockerfile.csi --platform linux/<arch> -t sds-csi:latest .
   docker save sds-csi:latest -o sds-csi.tar
   sudo k3s ctr images import sds-csi.tar      # or ctr -n k8s.io images import
   ```
   Where `registry.k8s.io` is unreachable, pull the sidecars from a mirror,
   retag them to the names in `deploy/k8s/20-controller.yaml` and
   `30-node.yaml`, and import them the same way.

2. Edit `deploy/k8s/00-sds-controller-endpoint.yaml` (the controller IP/VIP in
   the manual Endpoints) and the StorageClass `pool` in `40-storageclass.yaml`,
   then `kubectl apply -f deploy/k8s/`. `50-volumesnapshotclass.yaml` needs the
   snapshot CRDs (`deploy/k8s/README.md`).

3. Smoke test: `scripts/csi-e2e.sh` (PVC on StorageClass `sds-drbd`, a pod that
   writes, checks the pod landed on a replica node).

Both plugin pods use `hostNetwork` with `dnsPolicy: ClusterFirstWithHostNet`, so
they resolve the `sds-controller` Service through cluster DNS. Pods move between
replica nodes with their data; use a Deployment or StatefulSet and short
`tolerationSeconds` for `node.kubernetes.io/unreachable` / `not-ready` to fail
over faster than the 300 s default.

---

## 10. AI Copilot (`sds-ai`, optional)

Serves the web UI's Copilot. It runs `sds-mcp` as its tool backend and needs an
OpenAI-compatible LLM and embedder.

- Binaries on every node (it follows the controller): `/opt/sds/bin/sds-ai`,
  `/opt/sds/bin/sds-mcp`.
- Configuration on the Self-HA mount: `/var/lib/sds/ai/sds-ai.env` and
  `domain.toml`. Environment:
  - `STEWARD_LLM_API_KEY`, `STEWARD_LLM_BASE_URL`, `STEWARD_LLM_MODEL`,
    `STEWARD_EMB_*` (the older `OPSPILOT_*`, `OPSDOCTOR_*`, `OSS_*` names are
    still read)
  - `SDS_AI_KNOWLEDGE_DB` (required), `SDS_AI_EMB_DIM` (default 768)
  - `SDS_AI_CONTROLLER` — default `127.0.0.1:3374`, the controller beside it
  - `SDS_AI_MCP_CMD=/opt/sds/bin/sds-mcp`
  - `SDS_AI_ADDR` — default `127.0.0.1:7634`, which is where the UI proxies
    `/ai/`. On any non-loopback address sds-ai refuses to start without a token
    (`SDS_AI_TOKEN`, `SDS_TOKEN`, `~/.sds/token` or `/etc/sds/token`).
- `SDS_AI_EMB_DIM` must equal the dimension the index was built with. Switching
  to an embedder of a different width means rebuilding the knowledge base;
  otherwise searches return nothing, without an error. `GET /ai/kb/list` shows
  the width read from the index.
- Shared knowledge base: `make kb` (see `ai/kb/build.sh`) builds
  `dist/kb/sds-kb.db` and its manifest `sds-kb.json`. Install both at
  `/opt/sds/share/` on every node and set
  `SDS_AI_SHARED_KNOWLEDGE_DB=/opt/sds/share/sds-kb.db`; it is searched
  read-only next to `SDS_AI_KNOWLEDGE_DB`. Startup fails if the cluster's
  embedder model or dimension differs from the manifest's.
- Unit `sds-ai.service` (`EnvironmentFile`, `WorkingDirectory` and `HOME` =
  `/var/lib/sds/ai`), installed but **disabled**; list it in
  `[self_ha] extra_services` so the promoter starts it with the controller.
- HTTP: `GET /ai/health`, `POST /ai/chat/stream`, `GET /ai/kb/list`,
  `POST /ai/kb/{doc,ingest,refresh,purge}`. Send the same `session_id` in each
  chat request to continue a conversation.

`sds-mcp serve` exposes the same tools to remote MCP clients over HTTP with
token auth (default `127.0.0.1:43871`); `configs/sds-mcp-http.service` is a
reactor-managed unit for it. See [`mcp.md`](./mcp.md).

---

## 11. WAN replication (optional)

Replicates a resource to a DR site over a per-resource `sds-proxy` pair
(protocol A, mTLS). LAN resources are unaffected.

- `sds-proxy` on the controller at `/usr/local/bin/sds-proxy` is pushed to the
  WAN nodes; for nodes of another architecture place
  `/usr/local/bin/sds-proxy-<amd64|arm64>` beside it. Without a matching binary
  the controller assumes it is already installed on the node.
- The DR node's WAN port must be reachable over TCP from the primary site.
- The controller keeps the proxy CA under `[wan] pki_dir`
  (default `/var/lib/sds/wanproxy-pki`).

```bash
sds resource create --name data --port 7000 --nodes site1 --pool vg0 --size 10G \
    --wan --dr-node site2 --dr-endpoint <site2-public-ip> [--wan-port 37901]
sds resource status data
```

`--wan-port 0` (default) picks a random port above 3000. Failover to the DR site
is manual because replication is asynchronous:

```bash
sds resource dr-failover data --yes
```

`sds wan set-endpoint` and `sds wan repair` change or rebuild the
tunnels. Design: [`2026-07-05-wan-replication-design.md`](./2026-07-05-wan-replication-design.md).

---

## 12. Verify

```bash
sds node list
sds health-check
sds pool list
sds resource list
sds resource status <name>
sds ha self status
grpcurl -plaintext <host>:3374 list
curl -s http://<host>:3375/v1/nodes
# UI: http://<host-or-VIP>:3376/
```

On a node:

```bash
drbdadm status
drbd-reactorctl status
systemctl is-enabled sds-drbd-up.service
journalctl -u sds-controller -f
```

---

## 13. Known failure modes

| Symptom | Cause / fix |
| --- | --- |
| Remote ops fail: `unable to authenticate, attempted methods [none]` | Unit lacks `Environment="HOME=/root"`, or the dispatch host section is not keyed by IP (section 3). |
| Remote ops fail with `host key changed: <ip>` | The node's SSH host key changed. `ssh-keygen -R <ip> -f /root/.ssh/known_hosts` on the controller. |
| Controller exits at start: `dispatch config "<path>": stat …: no such file or directory` | `[dispatch] config_path` points at a missing file. |
| Reactor logs `IGNORING resource … expected ','` and never fails over | drbd-utils too old for drbd-reactor; match versions on all nodes. |
| Promoter fails: `Unit ocf.rs@….service not found` | drbd-reactor installed without its `ocf.rs@.service` template and wrapper (`node-prerequisites.md` §1). |
| Reactor won't start: `Could not read config file: /etc/drbd-reactor.toml` | Create it: `snippets = "/etc/drbd-reactor.d"` plus a `[[log]]` table. |
| Pool create fails with `Device or resource busy` | The disk is mounted or formatted: `umount`, remove from `/etc/fstab`, `wipefs -a <dev>`. |
| One node loss stops I/O on a 2-node resource | No tiebreaker: register a third node (with `auto_tiebreaker` on) or add a replica. |
| `gateway nfs create` fails with `missing: rpc.nfsd exportfs` | `apt-get install nfs-kernel-server` (EL: `nfs-utils`) on the resource's diskful nodes. |
| `gateway nvme create` fails loading `nvmet`/`nvmet-tcp` | `apt-get install linux-modules-extra-$(uname -r)`. |
| `gateway nvme create --transport rdma` fails with `no RDMA device` | The node has no RDMA NIC (or soft-RoCE link) under `/sys/class/infiniband`. |
| `pool add-cache` refuses with "not a thin pool" | Convert first: `sds pool convert-thin --node <n> --pool <p>`. |
| Backup fails: `rclone is required on <node>` | Install rclone on that node. |
| A gateway exports the wrong size (a 2 GiB resource serves ~1 GiB) | Gateway created by an older controller that exported the state volume. Delete and recreate it; the data volume was used as scratch, so its contents are lost. |
| A gateway will not move (`umount: /var/lib/sds/<r>: no mount point specified`) | Gateway created by an older controller with its state mount under `/var/lib/sds`, which Self-HA's mount covers. `gateway stop --resource <r>`, then `gateway start --resource <r>` moves it to `/var/lib/sds-gateway/<r>`; run `ha evict sds-meta` first if start targets a node whose old mount is hidden. |
| iSCSI/NFS clients get I/O errors on every switchover | Gateway created by an older controller raises its service IP before the target/exports. `gateway stop` then `gateway start` reorders it. |
| Copilot cites documents unrelated to the question | The knowledge base is nearly empty; check `GET /ai/kb/list` and ingest content. |
