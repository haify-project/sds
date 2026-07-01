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
- **NVMe-oF**: needs the `nvmet` kernel modules + `nvmetcli`. Not validated in
  this cluster (the nodes lacked the nvmet agents/tooling). Install before use.

---

## 5. HA configs & Self-HA VIP: the `service-ip` helper (all nodes)

HA configs (`MakeHa`) and Self-HA float a VIP via a systemd template
`service-ip@<IP>-<MASK>.service`, which runs the **`service-ip`** helper. This is
a separate Go project (not a distro package):

- Repo: `~/Things/dev/storage/service-ip` (module `service-ip`)
- Adds/removes a VIP on the auto-detected interface + sends Gratuitous ARP,
  OCF-style exit codes, `Type=oneshot` unit that stays `active (exited)`.

Build and install on every node:

```bash
cd ~/Things/dev/storage/service-ip
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/service-ip .
scp bin/service-ip <node>:/tmp/ && ssh <node> 'sudo install -m755 /tmp/service-ip /usr/local/bin/service-ip'
scp deployment/service-ip@.service <node>:/tmp/ && ssh <node> 'sudo mv /tmp/service-ip@.service /etc/systemd/system/service-ip@.service && sudo systemctl daemon-reload'
```

sds pre-flight-checks `/usr/local/bin/service-ip` before writing an HA VIP
config, so a missing helper now fails with a clear message instead of a silently
broken promoter.

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

dispatch config `/root/.dispatch/config.toml` on the controller (auto-adds host
keys, so no known_hosts pre-seed needed):

```toml
[ssh]
user = "root"
port = 22
key_path = "/root/.ssh/id_ed25519"
timeout = "30s"

[hosts.all]
addresses = ["<node1-ip>", "<node2-ip>", "<node3-ip>"]
```

Symptom when missing: node operations fail / "Permission denied (publickey)";
`sds-cli node register` shows nodes but health checks / resource ops error.

---

## 8. sds binaries

- **Controller node**: `sds-controller` at `/opt/sds/bin/sds-controller`, unit
  `configs/sds-controller.service`, config `/etc/sds/controller.toml`
  (default gRPC port **3374**; REST 3375, UI 3376, metrics per config).
- **All nodes** (convenience): `sds-cli` at `/usr/local/bin/sds-cli`.

Cross-compile for the nodes (linux/amd64) since the build host is often
darwin/arm64:

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/sds-controller ./cmd/controller
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/sds-cli ./cmd/cli
```

---

## 9. Storage pool (diskful nodes only)

Diskful nodes need a pool (VG/zpool) on a data disk, e.g. `sds_vg0` on
`/dev/sdc`. A **diskless quorum tiebreaker** node does NOT need a pool — it joins
the resource with `disk none` and stores no data.

```bash
sds-cli pool create --name vg0 --node <node> --disks /dev/sdc   # -> VG "sds_vg0"
```

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
| storage pool (`sds_vg0`) | diskful only | | ✅ | ✅ | ✅ |

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
