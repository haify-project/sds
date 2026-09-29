# SDS — Installation & Deployment Guide

An end-to-end walkthrough for installing and running SDS: from raw storage nodes
to a working DRBD-backed cluster with gateways, HA, Kubernetes CSI, the AI
Copilot, and optional WAN replication.

This guide is the "how to stand it up" companion to
[`node-prerequisites.md`](./node-prerequisites.md) (the exhaustive per-node
package/OCF-agent list). Read this top-to-bottom for a first install; jump to a
section for a specific capability.

Target OS in the reference clusters: **Ubuntu 24.04** (amd64 and arm64 both
validated). Adjust package names for other distros.

---

## 0. Architecture in one screen

```
sds-cli / web-ui / sds-ai ─┐
                           ▼ gRPC 3374 / REST 3375 / UI 3376
                    sds-controller  ──(dispatch / SSH as root)──►  storage nodes
                           │                                        ├─ DRBD 9 (kernel)
                           ├─ BBolt DB (resources, gateways, HA)    ├─ drbd-reactor (HA)
                           └─ drbd-reactor promoter configs          ├─ LVM / ZFS pools
                                                                     └─ OCF agents (gateways)
```

- **Control plane:** one `sds-controller` process. It owns a small BBolt database
  and drives every node **over SSH** using the `dispatch` library — there is no
  per-node SDS agent for storage ops. It exposes gRPC (`3374`), a REST/grpc-gateway
  (`3375`), an embedded web UI (`3376`), and Prometheus metrics.
- **Data plane:** DRBD 9 replicates block volumes between nodes; `drbd-reactor`
  promoters provide automatic failover; gateways (NFS / iSCSI / NVMe-oF) export a
  DRBD resource behind a floating VIP.
- **Optional:** a Kubernetes CSI driver (DRBD-backed PVs), the `sds-ai` Copilot,
  controller **Self-HA** (the controller itself floats on a DRBD-backed VIP), and
  opt-in **WAN replication** (DRBD over a per-resource `sds-proxy` pair).

The controller can run **on** one of the storage nodes or on a separate host; it
just needs root SSH to every node.

---

## 1. Build the binaries

On a build host with Go ≥ 1.25 and Node ≥ 20 (for the web UI):

```bash
git clone <sds-repo> && cd sds
make build            # builds bin/sds-controller and bin/sds-cli (UI embedded)
```

Cross-compile for the node arch when the build host differs (the binaries are
`CGO_ENABLED=0`, fully static):

```bash
# amd64 nodes
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/sds-controller ./cmd/controller
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/sds-cli        ./cmd/cli
# arm64 (信创/Kunpeng/…): swap GOARCH=arm64
```

The web UI is embedded into `sds-controller` via `go:embed ui/dist`. After a UI
change: `cd web-ui && npm run build` → `make ui-sync` → rebuild the controller.

Other binaries you may need later:
- `sds-mcp` (`./cmd/mcp`) — MCP tool backend for `sds-ai`.
- `csi-controller` / `csi-node` (`./cmd/csi-*`) — the Kubernetes CSI driver.
- `sds-ai` (`./cmd/sds-ai` — **its own Go module**: `cd cmd/sds-ai && go build .`).
- `sds-proxy` — the WAN transport, built from the separate `sds-proxy` repo.

---

## 2. Prepare the storage nodes (DRBD stack)

Do this on **every** storage node. Full detail (which OCF package provides what,
the version-match gotcha) is in `node-prerequisites.md`; the essentials:

1. **DRBD 9 kernel module + utils + reactor.**
   ```bash
   cat /proc/drbd            # want: version: 9.3.0 (or your 9.x)
   drbdadm --version         # DRBDADM_VERSION
   drbd-reactor --version
   ```
   Install DRBD 9 + `drbd-utils` + `drbd-reactor` from LINBIT's repo or a source
   build. **★ drbd-reactor and drbd-utils versions must match** — reactor parses
   `drbdsetup status --json`, and a too-old utils emits JSON reactor can't parse,
   so it silently stops managing the resource (`IGNORING resource … expected ','`)
   and never fails over. Match versions across all nodes (e.g. reactor 1.11 ↔
   drbd-utils 9.34).

2. **LVM (or ZFS)** for the backing pools: `apt-get install -y lvm2`.

3. **OCF resource agents** (for gateways AND HA):
   ```bash
   apt-get install -y resource-agents-extra    # Filesystem, nfsserver, exportfs, IPaddr2, nvmet-*, …
   ```
   `resource-agents-base` alone is NOT enough (it lacks `Filesystem`).

4. **Load DRBD at boot:** `echo drbd > /etc/modules-load.d/drbd.conf` (SDS also
   installs an `sds-drbd-up.service` on first resource create to `drbdadm up all`
   at boot).

5. Gateway-specific (only on nodes that will serve that gateway):
   - iSCSI: `apt-get install -y targetcli-fb python3-rtslib-fb`
   - NFS: `apt-get install -y nfs-kernel-server`
   - NVMe-oF: `apt-get install -y nvme-cli linux-modules-extra-$(uname -r)` then
     `modprobe nvmet nvmet-tcp` (the stock cloud kernel lacks the nvme-tcp modules).

6. **Real data disk(s)** for pools — a raw block device (`/dev/sdb`, `/dev/vdb`),
   not a loop file.

---

## 3. Root SSH trust + dispatch config

The controller runs as **root** and drives nodes over SSH. On the controller node:

```bash
sudo test -f /root/.ssh/id_ed25519 || \
  sudo ssh-keygen -t ed25519 -N "" -f /root/.ssh/id_ed25519 -C root@controller
sudo cat /root/.ssh/id_ed25519.pub
# append that pubkey to /root/.ssh/authorized_keys on EVERY node (incl. itself)
```

Write `/root/.dispatch/config.toml` on the controller node:

```toml
[ssh]
user = "root"
port = 22
key_path = "/root/.ssh/id_ed25519"
strict_host_key = false
known_hosts = ""          # ★ REQUIRED — see gotcha below
timeout = "30s"

# ★ Key these sections by IP ADDRESS, not by a friendly node name.
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

> **★ Key `[hosts.*]` by the node's IP address.** The controller asks dispatch
> for hosts by *address*, so a section named after a node (`[hosts.node1]`) never
> matches and dispatch falls through to `~/.ssh/config`. Any `Host` entry there
> whose `HostName` is that IP then supplies the user/port/key — and one address
> commonly has several aliases (frp tunnels: `Host x-frp … Port 10022`,
> `User someone-else`). The result is a connection with the wrong user or port
> and an `unable to authenticate, attempted methods [none]` that looks nothing
> like a config problem. Setting `user`/`key_path` inside the IP-keyed section
> makes it win: dispatch resolves TOML host > TOML group > `~/.ssh/config` >
> defaults.

> **★ The controller only reads this file if `[dispatch] config_path` points at
> it** (`controller.toml`). An unset path means dispatch's own default,
> `~/.dispatch/config.toml` of whatever user the controller runs as — which is
> not root's file when the controller does not run as root. A path that does not
> exist is now rejected at startup rather than silently ignored.

> **★ Set BOTH `strict_host_key = false` AND `known_hosts = ""`.** A node's SSH
> host key changes when it reboots (especially a hard power-off). With a stale
> known_hosts, dispatch silently rejects the new key and surfaces an *empty* error
> like `... creation failed on <ip>:` (nothing after the colon) while a shell
> `ssh` still works. Recovery: `rm /root/.ssh/known_hosts` + restart the
> controller. For a controller that runs behind Self-HA, keep this in the config
> so it survives node reboots.

For **Self-HA** the SSH trust must be **full-mesh** (any node may host the
controller): every node's root key in every node's `authorized_keys`.

---

## 4. Install & start the controller

```bash
sudo install -m755 bin/sds-controller /opt/sds/bin/sds-controller
sudo install -m755 bin/sds-cli        /usr/local/bin/sds-cli
sudo mkdir -p /etc/sds
```

`/etc/sds/controller.toml`:

```toml
[server]
listen_address = "0.0.0.0"
port = 3374

[dispatch]
config_path = "/root/.dispatch/config.toml"
parallel = 10
hosts = ["node1", "node2", "node3"]

[log]
level = "info"
format = "json"

[storage]
default_pool_type = "thin_pool"
default_snapshot_suffix = "_snap"
```

systemd unit `/etc/systemd/system/sds-controller.service`:

```ini
[Unit]
Description=SDS Controller
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
Environment=HOME=/root
ExecStart=/opt/sds/bin/sds-controller --config /etc/sds/controller.toml
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

> **★ `Environment=HOME=/root` is mandatory.** Under systemd there is no `$HOME`,
> so the dispatch SSH layer can't find `/root/.ssh/…` and every remote op fails
> with `unable to authenticate, attempted methods [none]`. (The local node works
> because it runs commands without SSH — only remote nodes expose this.)

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now sds-controller
systemctl is-active sds-controller            # active
ss -tlnp | grep -E ':(3374|3375|3376)'        # listening
```

The controller listens on gRPC `3374`, REST `3375`, UI `3376`. The `sds-cli`
defaults to `127.0.0.1:3374`; pass `--controller <host>:3374` from elsewhere.

---

## 5. Register nodes & create pools

```bash
sds-cli node register --name node1 --address <node1-ip>
sds-cli node register --name node2 --address <node2-ip>
sds-cli node register --name node3 --address <node3-ip>
sds-cli node list                              # all "online"
```

`--name` is a label of your choosing and does **not** have to equal the node's
hostname: registration records the real `uname -n`, and generated `.res` files
use that for the `on <name>` sections and the connection mesh (a DRBD resource
only applies to a host that finds itself there). Registering a host called
`lima-sds-a` as `node-a` is therefore fine.

A node that should never be picked as an automatic diskless quorum tiebreaker —
a WAN/DR site, whose public address is usually not even configured on its own
interface — must say so:

```bash
sds-cli node label <dr-node> sds.tiebreaker=false
```

Without it a 2-node resource can drag the DR node into its LAN connection mesh
and fail `drbdadm up` with `IP <addr> not found on this host`, after the backing
volumes have already been created.

If a node shows offline / health-check fails right after a reboot, it's usually
the SSH host-key gotcha (section 3): `rm /root/.ssh/known_hosts` + restart the
controller.

Create a storage pool on the data disk of each diskful node (VG name becomes
`sds_<name>`):

```bash
sds-cli pool create --name vg0 --nodes node1,node2,node3 --devices /dev/sdb --type lvm
sds-cli pool list                              # sds_vg0 on each node
```

A **diskless quorum tiebreaker** node needs no pool — it joins resources with
`disk none` and stores no data.

---

## 6. Create a replicated resource + filesystem

```bash
# a 2-node DRBD resource; the controller auto-adds a diskless tiebreaker if a
# spare node exists (so a single-node loss keeps quorum). --port is required.
sds-cli resource create --name data --port 7000 --nodes node1,node2 --size 10G --pool vg0
sds-cli resource status data                   # both peers UpToDate
```

Put a filesystem on it and mount it (one node at a time — DRBD is Primary-mounts).
Both take the volume id positionally (`fs <resource> <volume-id> <fstype>`,
`mount <resource> <volume-id> <mount-path>`):

```bash
sds-cli resource fs    data 0 ext4      --node node1
sds-cli resource mount data 0 /mnt/data --node node1
```

`resource status` shows per-node role/disk/replication state and, for WAN
resources, the DR endpoint and proxy health.

---

## 7. Gateways — export a resource to clients

A gateway is a DRBD resource + a `drbd-reactor` promoter that brings up a floating
**service IP** and the export service on whichever node holds the Primary. The VIP
is CIDR (`192.168.1.100/24`). Prerequisites per gateway type: section 2.5.

```bash
# NFS
sds-cli gateway nfs create --resource data --service-ip 192.168.1.200/24 --export-path /mnt/data

# iSCSI (LIO)
sds-cli gateway iscsi create --resource data \
    --iqn iqn.2026-01.com.example:sds.data --service-ip 192.168.1.100/24

# NVMe-oF (TCP, port 4420)
sds-cli gateway nvme create --resource data \
    --nqn nqn.2026-01.com.example:sds.data --service-ip 192.168.1.150/24
```

After creating a gateway, reload reactor on the nodes (`systemctl reload
drbd-reactor`) if it isn't auto-reloading. Verify the VIP is up and the target is
listening on the Primary node (`ss -tlnp | grep <port>`, `drbd-reactorctl status`).
The **`ocf.rs@.service` template + `/usr/libexec/drbd-reactor/ocf-rs-wrapper`**
must be installed (they ship with drbd-reactor) or the promoter fails to start.

---

## 8. High availability

### Per-resource HA (mount failover)

```bash
sds-cli ha create data --mount /mnt/data --fstype ext4 [--vip 192.168.1.210/24]
```

Reactor promotes the resource + mounts it on the surviving node when the Primary
dies. Quorum (`quorum majority` + `on-no-quorum io-error`) prevents split-brain;
a 2-diskful resource keeps quorum through a diskless tiebreaker (auto-added).

### Controller Self-HA (the controller itself becomes HA)

Makes `sds-controller` float on a DRBD-backed VIP so the control plane survives a
node loss. Requires **full-mesh root SSH**; the controller binary and the
`service-ip` helper + its systemd template are copied to the nodes by `enable`.

```bash
sds-cli ha self enable --pool vg0 --vip 192.168.1.250/24 [--port 7999 --size 1]
sds-cli ha self status                         # VIP, active node, members
sds-cli ha evict sds-meta                      # graceful move to a standby (test)
```

After Self-HA, the reactor-managed controller runs from **`/usr/local/bin/sds-controller`**
(not `/opt/sds/bin`). To ship a new build, overwrite `/usr/local/bin/sds-controller`
on **every** node and restart the active one (`mv` the old aside first — a running
binary can't be overwritten: `Text file busy`). Access everything via the VIP
(`http://<vip>:3376/`, `<vip>:3374`, etc.).

Extra services can ride the same promoter — set `[self_ha] extra_services =
["sds-ai.service"]` in `controller.toml` to make the AI Copilot follow the
controller across failover.

---

## 9. Kubernetes CSI driver (optional)

DRBD-backed PVs for k8s. The **worker nodes must be the DRBD storage nodes** (the
node plugin promotes/mounts DRBD on the host); the controller runs **outside** k8s,
reachable at an IP/VIP:3374.

1. Build the `sds-csi` image for the node arch (an amd64 image will NOT run on
   arm64) and load it into each node's containerd:
   ```bash
   GOOS=linux GOARCH=<arch> CGO_ENABLED=0 go build -o csi-controller ./cmd/csi-controller
   GOOS=linux GOARCH=<arch> CGO_ENABLED=0 go build -o csi-node       ./cmd/csi-node
   docker build --platform linux/<arch> -t sds-csi:latest .   # debian-slim + the 2 binaries
   docker save sds-csi:latest -o sds-csi.tar
   # on every node:  sudo k3s ctr images import sds-csi.tar   (or crictl/ctr on your runtime)
   ```
   The upstream sidecars (`registry.k8s.io/sig-storage/*`) may be unreachable in
   some regions — pull from a mirror (`registry.aliyuncs.com/google_containers/…`),
   retag to the original names, and `ctr images import` them too.

2. Deploy the manifests:
   ```bash
   kubectl apply -f deploy/k8s/
   ```
   Edit `deploy/k8s/00-sds-controller-endpoint.yaml` to carry the real controller
   IP/VIP (a selectorless Service + manual Endpoints make `sds-controller:3374`
   resolve to the external controller). Set the StorageClass `pool` to your pool
   name. The node DaemonSet uses `hostNetwork` and therefore
   `dnsPolicy: ClusterFirstWithHostNet` (already in the manifest) so it can resolve
   the controller Service name.

3. Smoke test: `scripts/csi-e2e.sh` (creates a PVC on StorageClass `sds-drbd` and a
   pod, verifies the write landed and the pod scheduled onto a replica node).

A PV is a DRBD volume replicated on `replicas` nodes: when a pod moves, the node
plugin promotes the local replica and mounts it, so the container restarts on
another node with its data intact. Use a Deployment/StatefulSet (a bare Pod won't
reschedule) and short `tolerationSeconds` for fast node-failure failover.

---

## 10. AI Copilot (`sds-ai`, optional)

A separate binary that serves the web-ui Copilot. It needs `sds-mcp` as its MCP
backend and an LLM/embedder (e.g. DashScope).

- Binaries on every node (so it can ride Self-HA): `/opt/sds/bin/{sds-ai,sds-mcp}`.
- Config on the Self-HA DRBD mount (so it follows failover): `/var/lib/sds/ai/`
  with `sds-ai.env` + `domain.toml`. Key env:
  `STEWARD_LLM_API_KEY/BASE_URL/MODEL`, `STEWARD_EMB_*`, `SDS_AI_EMB_DIM`
  (the older `OPSPILOT_*`, `OPSDOCTOR_*` and `OSS_*` names are still read),
  `SDS_AI_KNOWLEDGE_DB`,
  `SDS_AI_CONTROLLER=127.0.0.1:3374`, `SDS_AI_MCP_CMD=/opt/sds/bin/sds-mcp`,
  `SDS_AI_ADDR=:7634`.
- **The embedder must match the index it is searching**, and the model is not
  the constraint — the width is. Any OpenAI-compatible embedder works as long
  as `SDS_AI_EMB_DIM` equals the dimension the index was built at. Changing
  embedder to one of a different width means rebuilding the knowledge base:
  the old index cannot be searched with the new vectors, and the symptom is
  not an error but every search returning nothing. `GET /ai/kb/list` reports
  the width read back from the index, which is how you check.
- Unit `sds-ai.service` (`EnvironmentFile`/`WorkingDirectory`/`HOME` =
  `/var/lib/sds/ai`), left **disabled** so only the reactor promoter starts it.
- HTTP: `GET /ai/health`, `POST /ai/chat/stream` (chat), `GET /ai/kb/list`
  (what the knowledge base holds), and the knowledge-base update endpoints
  `POST /ai/kb/{doc,ingest,refresh,purge}`.
- The chat body's `session_id` is what makes a follow-up a follow-up. Send the
  same one across turns and the Copilot resolves "it" against what was already
  discussed; omit it and every turn starts from nothing.

Add `sds-ai.service` to `[self_ha] extra_services` so it rides the controller.

---

## 11. WAN replication (optional, opt-in)

Replicate a resource across the internet (primary site ↔ DR site) via a
per-resource `sds-proxy` pair (protocol A async + mTLS). The LAN default path is
untouched — every WAN flag is opt-in.

Prerequisites: the arm64/amd64 `sds-proxy` binary at `/usr/local/bin/sds-proxy` on
the participating nodes (the controller distributes it if present locally), and the
DR site's `wan-port` reachable over TCP from the primary's egress.

```bash
sds-cli resource create --name data --port 7000 --nodes site1 --pool vg0 --size 10G \
    --wan --dr-node site2 --dr-endpoint <site2-public-ip> [--wan-port 37901]

sds-cli resource status data          # WAN mode, DR endpoint, sds-proxy@data health, sync state
```

WAN is **asynchronous**, so failover to the DR site is a **manual** DR action (auto
promotion of a lagging secondary would risk data loss):

```bash
sds-cli resource dr-failover data --yes    # force-promotes the DR node (warns about the lossy window)
```

Design detail: [`2026-07-05-wan-replication-design.md`](./2026-07-05-wan-replication-design.md).

---

## 12. Verify & operate

```bash
sds-cli node list
sds-cli pool list
sds-cli resource list
sds-cli resource status <name>          # roles, disk states, replication, WAN health
sds-cli ha self status
grpcurl -plaintext <host>:3374 list     # gRPC introspection
curl -s http://<host>:3375/v1/nodes     # REST
# UI:  http://<host-or-VIP>:3376/
```

On the nodes:

```bash
drbdadm status                          # per-resource connection + disk states
drbd-reactorctl status                  # promoter targets and which node holds them
systemctl status drbd-reactor
journalctl -u sds-controller -f
```

Per-node readiness check (gateway/HA prerequisites present):

```bash
for p in /usr/lib/ocf/resource.d/heartbeat/Filesystem \
         /usr/lib/ocf/resource.d/heartbeat/IPaddr2 \
         /usr/local/bin/service-ip /etc/systemd/system/service-ip@.service; do
  test -e "$p" && echo "ok   $p" || echo "MISS $p"
done
```

---

## 13. Top gotchas (learned the hard way)

| Symptom | Cause / fix |
| --- | --- |
| Remote ops fail: `unable to authenticate, attempted methods [none]` | Controller systemd unit missing `Environment=HOME=/root`. |
| `... creation failed on <ip>:` (empty error), but shell `ssh` works | Stale SSH host key after a node reboot. Set `known_hosts = ""` in dispatch config; recover with `rm /root/.ssh/known_hosts` + restart controller. |
| Reactor logs `IGNORING resource … expected ','` and never fails over | `drbd-utils` too old for `drbd-reactor` — match versions across nodes. |
| Gateway/HA promoter fails: `Unit ocf.rs@…service not found` | `ocf.rs@.service` template + `ocf-rs-wrapper` not installed (ship with drbd-reactor). |
| Reactor won't start: `Could not read config file: /etc/drbd-reactor.toml` | Create the main config: `snippets = "/etc/drbd-reactor.d"` + `[[log]] level="info"`. |
| Pool create fails `Device or resource busy` on the data disk | The disk is formatted/mounted — `umount`, remove from `/etc/fstab`, `wipefs -a <dev>`. |
| A single node loss suspends I/O (no quorum) | 2-diskful resource with no tiebreaker. Register a 3rd node so the auto diskless tiebreaker can be added (or set `[resource] auto_tiebreaker`). |
| New resource stuck `Inconsistent`, gateway promote fails "Need access to UpToDate data" | Fresh DRBD needs an initial force-primary; recent controllers do this automatically on create. |
| CSI node plugin: every mount fails with gRPC `EOF` | hostNetwork DaemonSet needs `dnsPolicy: ClusterFirstWithHostNet`. |
| NVMe-oF gateway starts but no `:4420` listener | Missing `nvmet-tcp` kernel module — `apt-get install linux-modules-extra-$(uname -r)` + `modprobe`. |
| Distributing a large binary to a node silently fails | Fixed: `DistributeConfig` chunks large files (was capped by Linux `MAX_ARG_STRLEN`). Rebuild the controller if on an old version. |
| `gateway nfs create` reports success and prints mount instructions, but the gateway never starts | `nfs-kernel-server` is not installed on the nodes (section 4 of node-prerequisites). Creation does not preflight it; the failure appears only in `ocf.rs@nfsserver_*` as "No init script or systemd unit file detected for nfs server". |
| A gateway exports the wrong size — a 2 GiB resource serves ~1 GiB | A gateway created before the volume-role fix exported the cluster-private state volume and formatted the data volume as gateway scratch. Delete and recreate the gateway (`sds-cli gateway delete --resource <r>`, then create again); the data volume's contents are lost either way, since it was being used as scratch. |
| A gateway will not move (`ha evict` gives up after 20 s, the old node logs `umount: /var/lib/sds/<r>: no mount point specified`), or the controller will not | A gateway created before the fix kept its state mount under `/var/lib/sds`, where the controller's own Self-HA mount covers it. Run `sds-cli gateway stop --resource <r>` then `gateway start --resource <r>`: start moves it to `/var/lib/sds-gateway/<r>`. If start names a node whose old mount is hidden, `sds-cli ha evict sds-meta` first. |
| iSCSI or NFS clients get I/O errors on every switchover (`LUN not supported`, a filesystem remounted read-only, failed writes on a hard NFS mount) | A gateway created before the fix brings its service IP up before the target or exports, so a stop removes them while clients can still reach it. `gateway stop` then `gateway start` reorders it. |
| `pool add-cache` refuses with "not a thin pool" | lvmcache needs one LV every volume passes through; a thick pool has none. Convert first with `sds-cli pool convert-thin`. |
| The AI Copilot answers confidently but cites a document that has nothing to do with the question | The knowledge base is near-empty, so the single closest chunk is always the top hit. Check with `GET /ai/kb/list` and ingest real content. |
