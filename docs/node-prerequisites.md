# SDS Storage Node Prerequisites

What every storage node needs **beyond DRBD** for gateways and HA to actually
work. These were surfaced the hard way: gateways/HA configs reported "started"
while their drbd-reactor promoter silently failed because a required OCF agent
or helper was missing. sds now pre-flight-checks most of these, but the packages
still have to be installed on the nodes.

Target OS in this cluster: **Ubuntu 24.04**. Adjust package names for other
distros.

---

## 1. Base: DRBD (all nodes)

- DRBD kernel module 9.x (`cat /proc/drbd` → `version: 9.3.0`)
- `drbd-utils` (`drbdadm`, `drbdsetup`)
- `drbd-reactor` (`drbd-reactorctl`) — the promoter/HA engine

If missing, install DRBD 9 + drbd-utils + drbd-reactor from LINBIT's repo (or the
distro package). This cluster already had these on all nodes.

> **★ drbd-reactor and drbd-utils versions must match.** drbd-reactor runs
> `drbdsetup status --json <res>` and deserializes the output; a **drbd-utils that
> is too old emits a JSON shape reactor cannot parse**, so reactor logs
> `IGNORING resource '<res>': expected ',' or '}' at line NN` and **silently stops
> managing that resource** — the promoter never fails over. Seen on arm64 with
> drbd-utils **9.31.0** vs drbd-reactor **1.11.0**; installing drbd-utils
> **9.34.0** (the version the rest of this cluster runs) fixed it (`IGNORING` → 0).
> Diagnosis: it looks like a systemd problem (foreground reactor "works") but it is
> purely the utils version. Match `drbd-reactor --version` / `drbdadm --version`
> across all nodes.

> **★ A complete drbd-reactor install needs three pieces beyond the binary**, or
> gateways/HA fail to start:
> - `/etc/drbd-reactor.toml` — the main config (reactor **won't start** without it:
>   `Error: Could not read config file: /etc/drbd-reactor.toml`). Minimal:
>   `snippets = "/etc/drbd-reactor.d"` + `[[log]]\nlevel = "info"`.
> - `/lib/systemd/system/ocf.rs@.service` — the template systemd unit reactor uses
>   to run OCF agents. Missing → promoter start fails: `Unit ocf.rs@<...>.service
>   not found` and it loops trying to promote.
> - the `ocf-rs-wrapper` helper that `ocf.rs@.service` execs. **Its path is
>   packaging-dependent** — `/usr/bin/ocf-rs-wrapper` on Ubuntu 24.04 with
>   drbd-reactor 1.12 from LINBIT's PPA, `/usr/libexec/drbd-reactor/ocf-rs-wrapper`
>   with other builds. Do not check a hard-coded path; read the one the unit
>   actually uses: `grep ExecStart /lib/systemd/system/ocf.rs@.service`.
> All three come from a proper drbd-reactor package/`make install`; if you hand-copy
> a reactor binary between nodes, copy these too.

> **★ Install drbd-reactor with `-o Dpkg::Options::=--force-confnew`** (or make
> sure `/etc/drbd-reactor.toml` does not already exist). The package ships that
> file; if an earlier, interrupted setup left one behind, dpkg stops at an
> interactive conffile prompt. Over a non-interactive SSH session that fails with
> `end of file on stdin at conffile prompt`, the whole install rolls back, and the
> node is left with drbdadm but **no kernel module** — which `drbdadm --version`
> reports as `DRBD_KERNEL_VERSION=0`.

### DRBD boot unit (auto-up on reboot) — installed automatically

If nothing runs `drbdadm up all` at boot, a rebooted node brings up **none** of
its DRBD resources: they never reconnect, never re-sync, and drbd-reactor cannot
promote/mount them, so the node silently stays out of the cluster until an
operator runs `drbdadm adjust` by hand. This was found the hard way during
hard-failover testing.

You might expect the packaged **`drbd.service`** (Ubuntu 24.04 / DRBD 9) to
cover this, but it **cannot be enabled**: it is an LSB/SysV init script whose
`Default-Start` header is empty, so `systemctl enable drbd.service` fails with

```
update-rc.d: error: drbd Default-Start contains no runlevels, aborting
```

and the unit stays `disabled` no matter how many times you try. Enabling it is
therefore *not* a usable node prerequisite.

Instead, sds installs and enables its own native systemd oneshot on **resource
create** — best-effort and idempotent, on every participating node:

```ini
# /etc/systemd/system/sds-drbd-up.service
[Unit]
Description=Bring up all SDS DRBD resources at boot
After=network-online.target
Wants=network-online.target
Before=drbd-reactor.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/sbin/drbdadm up all

[Install]
WantedBy=multi-user.target
```

`ExecStart` uses whichever `drbdadm` path exists on the node (typically
`/sbin/drbdadm`). It runs `Before=drbd-reactor.service`, so on reboot the
resources are up before drbd-reactor tries to promote them. Verify with:

```bash
systemctl is-enabled sds-drbd-up.service   # -> enabled
```

Because sds installs this automatically, any node that already hosts an
sds-created resource auto-recovers on reboot with no operator action.

---

## 2. OCF resource agents (all nodes — REQUIRED for gateways AND HA)

Ubuntu 24.04 split the agents: `resource-agents-base` ships only a subset
(IPaddr2, iSCSITarget, ...) but **not `Filesystem`, `nfsserver`, `exportfs`**.
Every gateway type and every HA config starts with an `ocf:heartbeat:Filesystem`
mount, so without this package the promoter's first start action fails and the
whole target rolls back.

```bash
sudo apt-get install -y resource-agents-extra
```

Provides: `Filesystem`, `nfsserver`, `exportfs`, `portblock`, and more under
`/usr/lib/ocf/resource.d/heartbeat/`.

Symptom when missing: `ocf.rs@fs_cluster_private_<res>.service` fails; gateway
never brings up its VIP/target.

---

## 3. iSCSI gateway (nodes that will serve iSCSI)

The `iSCSITarget` OCF agent with `implementation=lio-t` needs the LIO userspace:

```bash
sudo apt-get install -y targetcli-fb python3-rtslib-fb
```

Symptom when missing: `ocf.rs@target_<res>.service` exits `5/NOTINSTALLED`.

---

## 4. NFS / NVMe gateways

- **NFS**: `nfsserver`/`exportfs` come from `resource-agents-extra` (section 2);
  also install the NFS server itself: `sudo apt-get install -y nfs-kernel-server`.
- **NVMe-oF** (validated on arm64): the promoter uses the `nvmet-subsystem`,
  `nvmet-namespace`, `nvmet-port` OCF agents (shipped in `resource-agents-extra`
  under `/usr/lib/ocf/resource.d/heartbeat/`). They drive nvmet directly via
  configfs (`/sys/kernel/config/nvmet/…`), so **`nvmetcli` is NOT required**
  (and is not in the Ubuntu 24.04 repos). What you DO need is the **nvmet-tcp /
  nvme-tcp kernel modules**, which the stock cloud/Lima kernel does **not** ship —
  they live in `linux-modules-extra`:

  ```bash
  # target nodes: nvmet + nvmet-tcp; initiator: nvme-tcp
  sudo apt-get install -y linux-modules-extra-$(uname -r) nvme-cli
  sudo modprobe nvmet nvmet-tcp   # target nodes
  sudo modprobe nvme-tcp          # initiator
  ```

  Symptom when missing: gateway starts but no `:4420` listener / `nvme connect`
  fails; `modprobe nvmet-tcp` errors with "module not found".

---

## 5. HA configs & Self-HA VIP: the `service-ip` helper (all nodes)

HA configs (`MakeHa`) and Self-HA float a VIP via a systemd template
`service-ip@<IP>-<MASK>.service`, which runs the **`service-ip`** helper. This is
a separate Go project (not a distro package):

- Repo: `~/Things/dev/storage/service-ip` (module `service-ip`)
- Adds/removes a VIP on the auto-detected interface + sends Gratuitous ARP,
  OCF-style exit codes, `Type=oneshot` unit that stays `active (exited)`.

Build and install on every node (set `GOARCH` to the node arch — `amd64` for
orange, `arm64` for the Lima/信创 clusters):

```bash
cd ~/Things/dev/storage/service-ip
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/service-ip .   # or GOARCH=arm64
scp bin/service-ip <node>:/tmp/ && ssh <node> 'sudo install -m755 /tmp/service-ip /usr/local/bin/service-ip'
scp deployment/service-ip@.service <node>:/tmp/ && ssh <node> 'sudo mv /tmp/service-ip@.service /etc/systemd/system/service-ip@.service && sudo systemctl daemon-reload'
```

sds pre-flight-checks `/usr/local/bin/service-ip` before writing an HA VIP
config, so a missing helper now fails with a clear message instead of a silently
broken promoter.

The reactor promoter references the unit as `service-ip@<IP>-<MASK>.service`
(e.g. `service-ip@192.168.104.101-24.service` for VIP `192.168.104.101/24`).

Symptom when missing: promoter fails with `Unit service-ip@<vip>.service not
found`; VIP never comes up.

---

## 6. Auto-reload drbd-reactor (all nodes — recommended)

So reactor reloads whenever sds adds/removes a config snippet, instead of relying
only on sds's explicit `systemctl reload`:

```bash
sudo cp /usr/share/doc/drbd-reactor/examples/drbd-reactor-reload.{path,service} /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now drbd-reactor-reload.path
```

---

## 7. Root SSH trust (controller → all nodes)

The controller runs as root and drives nodes over SSH (dispatch). Root on the
controller node needs key access to root on every node:

```bash
# on the controller node
sudo test -f /root/.ssh/id_ed25519 || sudo ssh-keygen -t ed25519 -N "" -f /root/.ssh/id_ed25519 -C root@<controller>
sudo cat /root/.ssh/id_ed25519.pub
# append that pubkey to /root/.ssh/authorized_keys on EVERY node (incl. the controller itself)
```

dispatch config `/root/.dispatch/config.toml` on the controller:

```toml
[ssh]
user = "root"
port = 22
key_path = "/root/.ssh/id_ed25519"
strict_host_key = false
known_hosts = ""          # ★ REQUIRED — see below
timeout = "30s"

[hosts.all]
addresses = ["<node1-ip>", "<node2-ip>", "<node3-ip>"]
```

Symptom when missing: node operations fail / "Permission denied (publickey)";
`sds-cli node register` shows nodes but health checks / resource ops error.

> **★ Set BOTH `strict_host_key = false` AND `known_hosts = ""`.** A node's SSH
> **host key changes when it reboots** (especially a hard power-off, e.g. HA
> failover testing with `limactl stop --force`). If the dispatch layer keeps a
> known_hosts with the node's old key, it silently rejects the new key — and it
> reports this as a **per-host failure with EMPTY output**, so the controller
> surfaces an opaque error like `backing volume ... creation failed on <ip>:`
> (nothing after the colon), while `ssh root@<ip>` from a shell still works
> (OpenSSH accepted the new key). `strict_host_key = false` alone is not enough if
> a stale known_hosts file is consulted; `known_hosts = ""` disables the file.
> Recovery for a node that already rotated its key: `rm /root/.ssh/known_hosts` on
> the controller + `systemctl restart sds-controller`.

---

## 8. sds binaries

- **Controller node**: `sds-controller` at `/opt/sds/bin/sds-controller`, unit
  `configs/sds-controller.service`, config `/etc/sds/controller.toml`
  (default gRPC port **3374**; REST 3375, UI 3376, metrics per config).
- **All nodes** (convenience): `sds-cli` at `/usr/local/bin/sds-cli`.

Cross-compile for the nodes since the build host is often darwin/arm64 (set
`GOARCH` to the node arch — `amd64` for orange, `arm64` for Lima/信创):

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/sds-controller ./cmd/controller
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/sds-cli ./cmd/cli
```

> **★ With controller Self-HA enabled**, the reactor-managed controller unit runs
> **`/usr/local/bin/sds-controller`** (Self-HA distributes the binary there so any
> node can host the controller), *not* `/opt/sds/bin/sds-controller`. To ship a
> new build you must overwrite `/usr/local/bin/sds-controller` on **every** node
> (all are potential active nodes) and restart the active one. Overwriting a
> running binary fails with `Text file busy` — `mv` the old one aside first, then
> `cp` the new one, then `systemctl restart sds-controller`.

### The embedded Web UI derives its API host at runtime

The controller embeds the built `web-ui` (`go:embed ui/dist`). The SPA computes
its API base from `window.location.hostname` (REST on `:3375`, AI Copilot on
`:7634`) — so the UI works from any node/VIP/tunnel with no rebuild. Do **not**
hardcode a specific host (an earlier `api.ts` special-cased `localhost` →
`http://orange1:3375`, which broke every non-orange deployment reached via
`localhost`/an SSH tunnel: the shell loaded but all data calls hit `orange1` and
failed with `ERR_EMPTY_RESPONSE`). Rebuild flow after a UI change:
`cd web-ui && npm run build` → `make ui-sync` (copies `web-ui/dist` → `ui/dist`
for the embed) → rebuild `sds-controller`.

---

## 8a. sds-ai — AI Copilot (optional; rides controller Self-HA)

`sds-ai` is a separate Go **submodule** (`cmd/sds-ai`, its own `go.mod`, depends
on `opsdoctor` — the library formerly published as `oss-agent`) that serves the
Copilot the Web UI talks to. It also needs
`sds-mcp` (built from `cmd/mcp`) as its MCP tool backend.

- Binaries on every node (any may become the active controller):
  `/opt/sds/bin/{sds-ai,sds-mcp}`.

  ```bash
  cd cmd/sds-ai && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o /tmp/sds-ai .   # or GOARCH=amd64
  GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o /tmp/sds-mcp ./cmd/mcp
  ```
- Config + knowledge live on the **Self-HA DRBD mount** so they follow failover:
  `/var/lib/sds/ai/` with `sds-ai.env` and `domain.toml`. Key env:
  `OPSDOCTOR_LLM_API_KEY` / `_BASE_URL` / `_MODEL` (e.g. DashScope
  `https://dashscope.aliyuncs.com/compatible-mode/v1` + `deepseek-v4-flash`),
  `SDS_AI_CONTROLLER=127.0.0.1:3374`, `SDS_AI_MCP_CMD=/opt/sds/bin/sds-mcp`,
  `SDS_AI_ADDR=:7634`. Routes: `GET /ai/health`, `POST /ai/chat/stream` (NDJSON).
- Unit `/etc/systemd/system/sds-ai.service` (`EnvironmentFile`/`WorkingDirectory`/
  `HOME` = `/var/lib/sds/ai`, `ExecStart=/opt/sds/bin/sds-ai`) installed on every
  node but left **`disabled`** — like `sds-controller`/`service-ip`, it is started
  only by the reactor promoter target, not at boot.
- **Make it follow the controller**: set `[self_ha] extra_services =
  ["sds-ai.service"]` in `controller.toml`; Self-HA appends it to the sds-meta
  promoter's start list so `sds-ai` starts/stops with the controller on the active
  node. (On an already-enabled cluster, add `"sds-ai.service"` after
  `"sds-controller.service"` in `/etc/drbd-reactor.d/sds-ha-sds-meta.toml` on all
  nodes and `systemctl reload drbd-reactor`; reactor then generates the
  `PartOf=drbd-services@sds-meta.target` / `BindsTo=drbd-promote@sds-meta.service`
  / `Requires=sds-controller.service` drop-in.) Verified: an `sds-cli ha evict
  sds-meta` moved controller + VIP + `sds-ai` together to the standby node.
- **Upgrading the binary is a failover, not a restart.** The reactor drop-in makes
  `sds-ai.service` `PartOf` the sds-meta target, so `systemctl restart sds-ai` on
  the active node stops the *whole* target — VIP, controller and all — and the
  resource is re-promoted wherever the race is won; the restart itself then fails
  with `Dependency failed for sds-ai.service`. Measured 2026-09-05: node-b →
  node-e, control plane down ~4 s. So install the new binary on **every** node
  first (keep the old one as `/opt/sds/bin/sds-ai.prev`), then either accept that
  failover or do it deliberately with `sds-cli ha evict sds-meta`. Never
  `systemctl stop sds-ai` — same teardown, without the automatic re-promotion.

---

## 9. Storage pool (diskful nodes only)

Diskful nodes need a pool (VG/zpool) on a data disk, e.g. `sds_vg0` on
`/dev/sdc`. A **diskless quorum tiebreaker** node does NOT need a pool — it joins
the resource with `disk none` and stores no data.

```bash
sds-cli pool create --name vg0 --node <node> --disks /dev/sdc   # -> VG "sds_vg0"
```

---

## 10. Reaching the UI/REST from a workstation

The controller listens on `0.0.0.0` (UI `3376`, REST `3375`, AI `7634`), so on a
**routable** network you just browse `http://<node-or-VIP>:3376/`.

On **Lima** the VMs sit on the `user-v2` network (`192.168.104.0/24` + the Self-HA
VIP), which is **reachable only VM-to-VM, not from the macOS host** (a host
`curl` to the VIP just fails). From the host you must SSH-tunnel — and forward
**all three** ports, because the SPA derives REST/AI from `window.location`
(`:3375` / `:7634`); forwarding only `3376` loads the shell with no data:

```bash
ssh -F ~/.lima/<vm>/ssh.config lima-<vm> -N -f \
  -L 34176:192.168.104.101:3376 \   # UI
  -L 3375:192.168.104.101:3375  \   # REST
  -L 7634:192.168.104.101:7634      # AI Copilot
# then open http://127.0.0.1:34176/
```

Tunnel to the **VIP** (not a node IP) through any up node, so the tunnel keeps
working after the controller fails over to another node.

---

## Quick per-node checklist

| Item | All nodes | Controller | iSCSI GW | NFS GW | HA/VIP |
| --- | :---: | :---: | :---: | :---: | :---: |
| DRBD 9 + drbd-utils + drbd-reactor | ✅ | ✅ | ✅ | ✅ | ✅ |
| `resource-agents-extra` (Filesystem) | ✅ | | ✅ | ✅ | ✅ |
| `targetcli-fb` + `python3-rtslib-fb` | | | ✅ | | |
| `nfs-kernel-server` | | | | ✅ | |
| `service-ip` helper + template | | | | | ✅ |
| `drbd-reactor-reload.path` | ✅ | | | | |
| root SSH trust + dispatch config | | ✅ | | | |
| `sds-controller` | | ✅ | | | |
| `sds-cli` | ✅ | ✅ | | | |
| `sds-ai` + `sds-mcp` (optional Copilot) | ✅¹ | | | | |
| storage pool (`sds_vg0`) | diskful only | | ✅ | ✅ | ✅ |

¹ Only if the AI Copilot is deployed; on every node so it can ride controller
Self-HA failover (section 8a).

## Verify a node is gateway/HA-ready

```bash
for p in \
  /usr/lib/ocf/resource.d/heartbeat/Filesystem \
  /usr/lib/ocf/resource.d/heartbeat/IPaddr2 \
  /usr/lib/ocf/resource.d/heartbeat/iSCSITarget \
  /usr/bin/targetcli \
  /usr/local/bin/service-ip \
  /etc/systemd/system/service-ip@.service; do
  test -e "$p" && echo "ok   $p" || echo "MISS $p"
done
```

---

## 11. Kubernetes CSI on the storage nodes (k3s example, arm64/信创)

The SDS CSI driver provisions DRBD-backed PVs. The **k8s worker nodes must BE the
DRBD storage nodes** (the node plugin runs privileged and promotes/mounts DRBD on
the host), and `sds-controller` runs **outside** k8s on those hosts (reachable at
an IP/VIP:3374). Validated end-to-end on a fresh 3-node arm64 (国产芯片) cluster.

### k3s (native arm64)

Install the server on one node, agents on the rest. Pin cluster networking to the
mutual node network and (in China) route image pulls through a proxy — set it in
the k3s service env so the embedded containerd inherits it. `NO_PROXY` **must**
include the cluster/service CIDRs and the node subnet, or internal traffic breaks.

```bash
# /etc/systemd/system/k3s.service.env  (and k3s-agent.service.env on agents)
HTTP_PROXY=http://<proxy>:7890
HTTPS_PROXY=http://<proxy>:7890
NO_PROXY=127.0.0.1,localhost,10.42.0.0/16,10.43.0.0/16,<node-subnet>/24,.svc,.cluster.local

# server
curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="server \
  --node-ip <ip> --advertise-address <ip> --flannel-iface eth0 \
  --write-kubeconfig-mode 644 --disable traefik --disable servicelb" sh -
# agent
curl -sfL https://get.k3s.io | K3S_URL=https://<server-ip>:6443 K3S_TOKEN=<token> \
  INSTALL_K3S_EXEC="agent --node-ip <ip> --flannel-iface eth0" sh -
```

### Images — build the plugin for the node arch, sideload the sidecars

The `sds-csi` image must match the node arch — an amd64 image will NOT run on
arm64 nodes. Build it for the target arch and import into every node's containerd:

```bash
# cross-build the two binaries (host go), assemble a minimal arm64 image, save, import
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o csi-controller ./cmd/csi-controller
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o csi-node       ./cmd/csi-node
docker build --platform linux/arm64 -t sds-csi:latest .    # debian-slim + the 2 binaries
docker save sds-csi:latest -o sds-csi.tar
# on every node:
sudo k3s ctr images import sds-csi.tar
```

The upstream **sidecars live on `registry.k8s.io`, which redirects to
`*.pkg.dev` and is often unreachable in China (pull fails with `EOF`)** — even via
an HTTP proxy. Pull them from a mirror, retag to the original names, save, and
`k3s ctr images import` on every node (default pull policy IfNotPresent then finds
them locally):

```bash
M=registry.aliyuncs.com/google_containers ; K=registry.k8s.io/sig-storage
for t in csi-provisioner:v5.1.0 csi-node-driver-registrar:v2.12.0 livenessprobe:v2.14.0; do
  docker pull --platform linux/arm64 $M/$t && docker tag $M/$t $K/$t
done
docker save $K/csi-provisioner:v5.1.0 $K/csi-node-driver-registrar:v2.12.0 $K/livenessprobe:v2.14.0 -o csi-sidecars.tar
```

### Deploy + point the CSI at the controller

`kubectl apply -f deploy/k8s/`. Edit `00-sds-controller-endpoint.yaml` to carry the
real controller IP/VIP (selectorless Service + manual Endpoints make
`sds-controller:3374` resolve to the external controller). Set the StorageClass
`pool` to the real pool name (`vg0`).

**★ The node DaemonSet uses `hostNetwork: true` and therefore needs
`dnsPolicy: ClusterFirstWithHostNet`** (fixed in `30-node.yaml`). Without it the
hostNetwork pod uses the node's resolv.conf, cannot resolve the `sds-controller`
Service name, and every `MountDevice` fails with a gRPC `EOF` — while the CSI
*controller* (not hostNetwork) works, so provisioning succeeds but mounting hangs.

### Cross-node PV behavior (the point of DRBD-CSI)

A PV is a DRBD volume replicated on `replicas` nodes. When a pod moves to another
node, the node plugin promotes the **local** replica to Primary and mounts it — the
data is already there, so the container restarts on a different node with its data
intact (verified: wrote on node B, re-read the same bytes from a pod pinned to node
A). Constraints: **RWO** = one node mounts at a time (move = demote here, promote
there); **topology** = the pod only schedules onto a node that holds a replica
(`WaitForFirstConsumer` + `--strict-topology` enforce this). This is the advantage
over k3s local-path, whose data is pinned to a single node.

### HA under a hard node failure (validated with PostgreSQL)

A `postgres:16-alpine` Deployment (RWO PVC, `strategy: Recreate`,
`PGDATA=/var/lib/postgresql/data/pgdata` so it does not trip on the volume's
`lost+found`) survived a `limactl stop --force` of the node running it: the DRBD
volume kept quorum via the diskless tiebreaker (2/3), auto-promoted on a survivor,
and k8s recreated the pod on another node with all committed rows intact and the
DB writable again — ~80s end to end. Speed-up: set the pod's
`node.kubernetes.io/unreachable` + `not-ready` tolerations to a short
`tolerationSeconds` (default is 300s / 5 min). A bare Pod does NOT reschedule — a
workload controller (Deployment/StatefulSet) is required.

### Smoke test

`scripts/csi-e2e.sh` (PVC on StorageClass `sds-drbd` + a pod that writes a marker,
then verifies the write landed and the pod scheduled onto a replica node).
