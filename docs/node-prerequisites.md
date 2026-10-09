# Haify Storage Node Prerequisites

What each node needs, and what goes wrong without it. Most of these failures do
not surface where the command was run: a drbd-reactor promoter config is
accepted whether or not the agents and units it names exist, so a missing piece
shows up later as a promoter that never starts.

Haify checks some of this itself — gateway creation checks OCF agents,
`targetcli` and the NFS server, `ha self enable` checks drbd-reactor and SSH, backups check
`rclone`, `replication-tls setup` checks TLS support — but installs none of the
packages. `sds health-check` reports DRBD, drbd-reactor (installed, running)
and resource-agents per node.

Package names are Ubuntu 24.04's. Installation order and controller setup:
[`deployment-guide.md`](./deployment-guide.md).

---

## 1. DRBD and drbd-reactor (all nodes)

- DRBD 9 kernel module (`cat /proc/drbd` → `version: 9.x`)
- `drbd-utils` (`drbdadm`, `drbdsetup`)
- `drbd-reactor` (`drbd-reactorctl`), enabled and running

Install from LINBIT's repositories or source builds.

**Keep drbd-utils and drbd-reactor versions matched.** drbd-reactor parses
`drbdsetup status --json <res>`. A drbd-utils too old for the reactor emits JSON
it cannot parse; reactor logs `IGNORING resource '<res>': expected ',' or '}' at
line NN` and stops managing that resource, so it never fails over. Seen with
drbd-utils 9.31.0 and drbd-reactor 1.11.0; drbd-utils 9.34.0 fixed it. A
foreground reactor appears to work, which makes it look like a systemd problem.

**A working drbd-reactor install has three pieces besides the binary:**
- `/etc/drbd-reactor.toml`. Without it reactor does not start
  (`Could not read config file: /etc/drbd-reactor.toml`). Minimal content:
  ```toml
  snippets = "/etc/drbd-reactor.d"
  [[log]]
  level = "info"
  ```
- `/lib/systemd/system/ocf.rs@.service`, the template reactor runs OCF agents
  through. Missing → `Unit ocf.rs@<...>.service not found` and the promoter
  loops.
- the `ocf-rs-wrapper` that unit executes. Its path depends on packaging
  (`/usr/bin/ocf-rs-wrapper` with drbd-reactor 1.12 from LINBIT's PPA on Ubuntu
  24.04, `/usr/libexec/drbd-reactor/ocf-rs-wrapper` elsewhere); read it from
  `grep ExecStart /lib/systemd/system/ocf.rs@.service`.

All three come with the package. A reactor binary copied between nodes needs
them copied too.

**Install drbd-reactor non-interactively with
`-o Dpkg::Options::=--force-confnew`** when `/etc/drbd-reactor.toml` may already
exist. Otherwise dpkg stops at the conffile prompt, fails over SSH with
`end of file on stdin at conffile prompt`, and rolls the whole transaction back;
the node is left with drbdadm but no kernel module (`drbdadm --version` shows
`DRBD_KERNEL_VERSION=0`).

### Boot-time bring-up: installed by Haify

Nothing in the packages brings DRBD resources up at boot. The packaged
`drbd.service` on Ubuntu 24.04 is an LSB script with an empty `Default-Start`, so
`systemctl enable drbd.service` fails (`update-rc.d: error: drbd Default-Start
contains no runlevels, aborting`). A node that reboots without a bring-up step
rejoins no resource until someone runs `drbdadm adjust`.

`resource create` installs and enables `sds-drbd-up.service` on every node of the
resource. It runs `/usr/local/sbin/sds-drbd-up.sh`, ordered `Before=drbd-reactor.service`,
which:
1. activates LVM (`vgchange -ay`) so backing LVs exist before DRBD attaches;
2. opens the LUKS containers of encrypted resources on that node;
3. runs `drbdadm adjust` per resource, ignoring failures of individual
   resources (one already brought up elsewhere does not stop the rest).

```bash
systemctl is-enabled sds-drbd-up.service   # enabled
```

---

## 2. OCF resource agents (gateway nodes)

```bash
sudo apt-get install -y resource-agents-extra
```

Ubuntu 24.04's `resource-agents-base` has `IPaddr2` and the iSCSI agents but not
`Filesystem`, `nfsserver`, `exportfs` or the `nvmet-*` agents. Every gateway
promoter starts with `ocf:heartbeat:Filesystem` and raises its service IP with
`ocf:heartbeat:IPaddr2`. `ha create` and Self-HA do not use OCF agents: they
mount through a systemd `.mount` unit and raise the VIP through
`service-ip@.service` (§4).

Gateway creation checks, on the resource's diskful nodes, under
`/usr/lib/ocf/resource.d/heartbeat/`:

| Gateway | Agents | Tools |
| --- | --- | --- |
| NFS | `Filesystem`, `IPaddr2`, `nfsserver`, `exportfs` | `rpc.nfsd`, `exportfs` |
| iSCSI | `Filesystem`, `IPaddr2`, `iSCSITarget`, `iSCSILogicalUnit` | `targetcli` |
| NVMe-oF | `Filesystem`, `IPaddr2`, `nvmet-subsystem`, `nvmet-namespace`, `nvmet-port` | |

---

## 3. Gateway userspace (nodes of the exported resource)

- **iSCSI**: `sudo apt-get install -y targetcli-fb python3-rtslib-fb`. The
  `iSCSITarget` agent (`implementation=lio-t`) needs the LIO userspace; without
  it `ocf.rs@target_<res>.service` exits `5/NOTINSTALLED`. LIO is the only
  implementation Haify accepts; `--implementation tgt` and `iet` are refused.
- **NFS**: `sudo apt-get install -y nfs-kernel-server` (EL: `nfs-utils`).
  Creation fails with `missing: rpc.nfsd exportfs` without it; otherwise the
  gateway would be created and never start, with `ocf.rs@nfsserver_*` logging
  "No init script or systemd unit file detected for nfs server".
- **NVMe-oF**: the `nvmet-*` agents work through configfs
  (`/sys/kernel/config/nvmet`); `nvmetcli` is not used. The kernel modules are:
  Ubuntu cloud kernels ship `nvmet-tcp` only in `linux-modules-extra`.
  ```bash
  sudo apt-get install -y linux-modules-extra-$(uname -r) nvme-cli
  ```
  Gateway creation loads `nvmet` and `nvmet-tcp` (`nvmet-rdma` for
  `--transport rdma`, which also needs a device under `/sys/class/infiniband`),
  adds them to `/etc/modules-load.d/nvmet.conf`, and fails if they cannot load.
  Initiators need `nvme-tcp` (`modprobe nvme-tcp`) and `nvme-cli`.

---

## 4. VIPs: the `service-ip` helper (nodes of an HA resource with a VIP, Self-HA nodes)

`ha create --vip` and Self-HA raise their VIP through `service-ip@<IP>-<MASK>.service`
(e.g. `service-ip@192.168.1.210-24.service`), which executes
`/usr/local/bin/service-ip up|down`: it adds or removes the address on the
detected interface and sends gratuitous ARP. Sources: `cmd/service-ip`,
`configs/service-ip@.service`.

`ha create --vip` and `ha self enable` install both on nodes that lack them,
copying the binary found next to the controller (or `/usr/local/bin/service-ip`
on the controller node). A node of a different architecture is refused; build
and install it there by hand:

```bash
GOOS=linux GOARCH=<arch> go build -o service-ip ./cmd/service-ip
sudo install -m 755 service-ip /usr/local/bin/service-ip
sudo cp configs/service-ip@.service /etc/systemd/system/ && sudo systemctl daemon-reload
```

Missing on a node → `Unit service-ip@<vip>.service not found`, VIP never comes up.

---

## 5. Storage (diskful nodes)

- `lvm2` for LVM pools; `zfsutils-linux` for ZFS pools.
- A data disk with no filesystem, partition table or mount. Pool creation on a
  used disk fails with `Device or resource busy`; clear it with `umount`, remove
  it from `/etc/fstab`, `wipefs -a <dev>`.
- `thin-provisioning-tools` on nodes whose resources are backed up from thin
  pools: incremental backups run `thin_delta`.

```bash
sds pool create --name vg0 --nodes <node> --devices /dev/sdc   # VG sds_vg0
```

A diskless quorum tiebreaker node needs no pool.

---

## 6. SSH access (controller → all nodes)

The controller runs node commands over SSH (dispatch) with `sudo`, so the login
user is root or has passwordless sudo. With Self-HA, passwordless **root** SSH
must work between every pair of nodes, and the key named in the dispatch config
must exist on every node. Key setup and the dispatch config:
[`deployment-guide.md` §3](./deployment-guide.md#3-ssh-trust-and-dispatch-config).

A node whose SSH host key changed (rebuilt, or a VM that regenerates keys) is
rejected with `host key changed: <ip>`. Remove the stale entry on the controller
node: `ssh-keygen -R <ip> -f /root/.ssh/known_hosts`.

---

## 7. Haify binaries

| Binary | Where | Notes |
| --- | --- | --- |
| `sds-controller` | `/opt/sds/bin/` on the controller node; on every Self-HA node | unit `configs/sds-controller.service`, config `/etc/sds/controller.toml` |
| `service-ip` | `/usr/local/bin/` | installed automatically where needed (§4) |
| `sds` | `/usr/local/bin/` (any node or workstation) | |
| `sds-ai`, `sds-mcp` | `/opt/sds/bin/` on every Self-HA node | only with the AI Copilot (§10) |
| `sds-proxy` | `/usr/local/bin/` on WAN nodes | pushed by the controller when it has a matching binary |

Build for the node architecture (`GOOS=linux GOARCH=amd64|arm64 CGO_ENABLED=0`).

With Self-HA, `ha self enable` copies the running controller binary to the path
the unit's `ExecStart` names on the other nodes; a node of another architecture
needs `sds-controller-<goarch>` beside the running binary. A new build has to be
installed there on every node, then moved onto with `sds ha evict sds-meta`
(`deployment-guide.md` §8).

---

## 8. Encrypted replication (optional; all nodes of an encrypted resource)

- DRBD 9.2 or later with TLS support (`drbdsetup net-options --help` lists `--tls`)
- the kernel `tls` module (`CONFIG_TLS`); Ubuntu 24.04's 6.8 kernel has it
- `ktls-utils` for `tlshd`: `apt install ktls-utils` / `dnf install ktls-utils`
- `openssl`, and a system trust store (`update-ca-certificates` or `update-ca-trust`)

`sds replication-tls setup` checks each of these and names what is missing
per node, then creates a node key, issues a certificate from the controller's
replication CA, adds that CA to the trust store, writes `/etc/tlshd.conf`
(keeping the original as `/etc/tlshd.conf.sds-orig`), loads `tls` at boot and
enables `tlshd`. `sds resource tls <resource> on` then switches a resource.

## 9. Other optional features

- **Backups**: `rclone` on every diskful node of a backed-up resource (the
  transfer runs on a replica node). Checked before each backup:
  `rclone is required on <node> and was not found`.
- **Encryption at rest** (`resource create --encrypt`, LVM pools only):
  `cryptsetup` and the `dm-crypt` module on every node of the resource; checked
  before anything is created.

---

## 10. sds-ai (optional; every Self-HA node)

`sds-ai` (`cmd/sds-ai`, a separate Go module built on `steward`) serves the web
UI's Copilot and runs `sds-mcp` as its tool backend.

```bash
cd cmd/sds-ai && GOOS=linux GOARCH=<arch> CGO_ENABLED=0 go build -o sds-ai .
GOOS=linux GOARCH=<arch> CGO_ENABLED=0 go build -o sds-mcp ./cmd/mcp   # from the repo root
```

- Binaries: `/opt/sds/bin/sds-ai`, `/opt/sds/bin/sds-mcp` on every node.
- Config on the Self-HA mount so it follows failover: `/var/lib/sds/ai/sds-ai.env`
  and `domain.toml`. Environment variables: `deployment-guide.md` §10. Leave
  `SDS_AI_ADDR` at its default `127.0.0.1:7634`: the controller's UI proxies
  `/ai/` to that address on its own node, and on a non-loopback address sds-ai
  refuses to start without a token.
- Unit `/etc/systemd/system/sds-ai.service`, written by hand (the repository
  ships none): `EnvironmentFile=/var/lib/sds/ai/sds-ai.env`,
  `WorkingDirectory` and `HOME` = `/var/lib/sds/ai`,
  `ExecStart=/opt/sds/bin/sds-ai`; on every node, **disabled**: only the
  promoter starts it.
- To make it follow the controller, set `[self_ha] extra_services =
  ["sds-ai.service"]` before `ha self enable`. On a cluster where Self-HA is
  already enabled, add `"sds-ai.service"` after `"sds-controller.service"` in
  `/etc/drbd-reactor.d/sds-ha-sds-meta.toml` on every node and
  `systemctl reload drbd-reactor`.
- **Restarting it is a failover.** drbd-reactor makes every service in the
  promoter `PartOf` the `sds-meta` target, so `systemctl restart sds-ai` on the
  active node stops the whole target — VIP and controller included — and the
  resource is promoted again wherever the race is won (a few seconds without
  a controller). `systemctl stop sds-ai` tears the target down the same way.
  Install a new binary on every node first (keep the old one as
  `/opt/sds/bin/sds-ai.prev`), then move deliberately with
  `sds ha evict sds-meta`.

---

## 11. Reaching the UI

The UI listens on `[ui] port` (default 3376) on `[ui] listen_address` (default:
the gRPC listen address). It proxies `/v1/` to the REST gateway and `/ai/` to
sds-ai on its own node, so one port is enough. When the controller address is
not routable from a workstation, tunnel that port to the VIP through any node:

```bash
ssh -N -L 3376:<vip>:3376 <any-node>
# then open http://127.0.0.1:3376/
```

Tunnelling to the VIP rather than a node address keeps the tunnel working after
the controller fails over.

---

## Per-node checklist

| Item | All nodes | Controller / Self-HA nodes | iSCSI | NFS | NVMe-oF | HA with VIP |
| --- | :---: | :---: | :---: | :---: | :---: | :---: |
| DRBD 9 + drbd-utils + drbd-reactor | yes | yes | yes | yes | yes | yes |
| `resource-agents-extra` | | | yes | yes | yes | |
| `targetcli-fb`, `python3-rtslib-fb` | | | yes | | | |
| `nfs-kernel-server` | | | | yes | | |
| `linux-modules-extra` (nvmet-tcp) | | | | | yes | |
| `service-ip` + `service-ip@.service` | | yes (auto) | | | | yes (auto) |
| `lvm2` + data disk | diskful only | | | | | |
| SSH key + dispatch config | | yes | | | | |
| `sds-controller` | | yes | | | | |

Optional: `rclone` (backups), `thin-provisioning-tools` (incremental backups),
`ktls-utils` + `openssl` (encrypted replication), `cryptsetup` (encryption at
rest), `sds-ai` + `sds-mcp` (Copilot).

```bash
for p in \
  /usr/lib/ocf/resource.d/heartbeat/Filesystem \
  /usr/lib/ocf/resource.d/heartbeat/IPaddr2 \
  /usr/lib/ocf/resource.d/heartbeat/iSCSITarget \
  /lib/systemd/system/ocf.rs@.service \
  /etc/drbd-reactor.toml \
  /usr/local/bin/service-ip \
  /etc/systemd/system/service-ip@.service; do
  test -e "$p" && echo "ok   $p" || echo "MISS $p"
done
```

---

## 12. Kubernetes CSI on the storage nodes (k3s example)

The Kubernetes nodes must be the DRBD storage nodes: the node plugin runs
privileged and promotes and mounts DRBD on the host. `sds-controller` runs
outside Kubernetes, reachable at an IP or VIP on 3374.

### k3s

Pin the node IP and flannel interface to the network the nodes share. When image
pulls go through a proxy, set it in the k3s service environment so containerd
inherits it, and keep cluster traffic out of it with `NO_PROXY`:

```bash
# /etc/systemd/system/k3s.service.env (k3s-agent.service.env on agents)
HTTP_PROXY=http://<proxy>:<port>
HTTPS_PROXY=http://<proxy>:<port>
NO_PROXY=127.0.0.1,localhost,10.42.0.0/16,10.43.0.0/16,<node-subnet>/24,.svc,.cluster.local

# server
curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="server \
  --node-ip <ip> --advertise-address <ip> --flannel-iface eth0 \
  --write-kubeconfig-mode 644 --disable traefik --disable servicelb" sh -
# agents
curl -sfL https://get.k3s.io | K3S_URL=https://<server-ip>:6443 K3S_TOKEN=<token> \
  INSTALL_K3S_EXEC="agent --node-ip <ip> --flannel-iface eth0" sh -
```

### Images

The `sds-csi` image must match the node architecture. `Dockerfile.csi`
cross-compiles both plugins in its build stage:

```bash
docker build -f Dockerfile.csi --platform linux/<arch> -t sds-csi:latest .
docker save sds-csi:latest -o sds-csi.tar
sudo k3s ctr images import sds-csi.tar        # on every node
```

The sidecars come from `registry.k8s.io`, which can be unreachable (pulls fail
with `EOF`). Pull them from a mirror, retag to the original names and import
them on every node; the default `IfNotPresent` pull policy for tagged images
then uses the local copies. The images and tags are those in `deploy/k8s/20-controller.yaml` and
`30-node.yaml`:

```bash
M=<mirror-registry>/<path> ; K=registry.k8s.io/sig-storage
for t in csi-provisioner:v5.1.0 csi-resizer:v1.13.2 csi-snapshotter:v8.2.0 \
         csi-node-driver-registrar:v2.12.0 livenessprobe:v2.14.0; do
  docker pull --platform linux/<arch> $M/$t && docker tag $M/$t $K/$t
done
docker save $(for t in csi-provisioner:v5.1.0 csi-resizer:v1.13.2 csi-snapshotter:v8.2.0 \
  csi-node-driver-registrar:v2.12.0 livenessprobe:v2.14.0; do echo $K/$t; done) -o csi-sidecars.tar
```

### Deploy

Edit `deploy/k8s/00-sds-controller-endpoint.yaml` so its Endpoints carry the
controller IP/VIP (a selectorless Service plus manual Endpoints makes
`sds-controller:3374` resolve to the external controller), set `pool` in
`40-storageclass.yaml`, then `kubectl apply -f deploy/k8s/`.
`50-volumesnapshotclass.yaml` needs the snapshot CRDs (`deploy/k8s/README.md`).

Both plugin pods run with `hostNetwork: true` and
`dnsPolicy: ClusterFirstWithHostNet`. Without that DNS policy a hostNetwork pod
uses the node's resolv.conf, cannot resolve `sds-controller`, and every call
fails with a gRPC `EOF`.

### Behaviour

A PV is a DRBD volume on `replicas` nodes. When a pod moves, the node plugin
promotes the local replica and mounts it, so the data is already there.
Volumes are RWO (one node at a time); `WaitForFirstConsumer` with
`--strict-topology` schedules pods only onto replica nodes.

A node failure is survived when the volume keeps quorum (two replicas plus the
diskless tiebreaker) and the workload is a Deployment or StatefulSet; a bare Pod
is not rescheduled. Kubernetes waits 300 s before evicting from an unreachable
node; set short `tolerationSeconds` for `node.kubernetes.io/unreachable` and
`node.kubernetes.io/not-ready` to fail over sooner. For PostgreSQL, set
`PGDATA` to a subdirectory of the mount (the volume root holds `lost+found`).

Smoke test: `scripts/csi-e2e.sh`.
