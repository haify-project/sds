# SDS user guide

This is the day-to-day guide: how to think about SDS, and how to carry out the
things you will actually do with it. It assumes a cluster that is already up —
see [deployment-guide.md](deployment-guide.md) to build one and
[node-prerequisites.md](node-prerequisites.md) for what each node needs
installed.

Every command here is `sds-cli`, which talks to the controller over gRPC on
port 3374. The same operations are available in the web UI and, for AI
assistants, through [`sds-mcp`](mcp.md).

---

## Contents

1. [The mental model](#1-the-mental-model)
2. [Talking to the cluster](#2-talking-to-the-cluster)
3. [Nodes](#3-nodes)
4. [Pools](#4-pools)
5. [Resources — your storage](#5-resources--your-storage)
6. [Using a resource](#6-using-a-resource)
7. [Changing a resource's shape](#7-changing-a-resources-shape)
8. [Snapshots](#8-snapshots)
9. [Backups — the only copy that survives losing the cluster](#9-backups--the-only-copy-that-survives-losing-the-cluster)
10. [Gateways — exporting to clients](#10-gateways--exporting-to-clients)
11. [High availability](#11-high-availability)
12. [Cross-site replication (WAN DR)](#12-cross-site-replication-wan-dr)
13. [Storage tiering](#13-storage-tiering)
14. [Encryption](#14-encryption)
15. [Alerts and notifications](#15-alerts-and-notifications)
16. [Access control](#16-access-control)
17. [Kubernetes](#17-kubernetes)
18. [The AI Copilot](#18-the-ai-copilot)
19. [Routine operations](#19-routine-operations)
20. [When something is wrong](#20-when-something-is-wrong)

---

## 1. The mental model

Four things, stacked:

```
node          a Linux machine SDS reaches over SSH
 └─ pool      an LVM volume group or ZFS pool on that node's disks
     └─ resource   a replicated block device: the same bytes on several nodes
         └─ volume     one block device inside the resource (usually just one)
```

A **resource** is the unit that matters. It is a DRBD device: you write to it on
one node and the bytes land on every replica synchronously. One replica is
**Primary** (mounted, writable); the others are **Secondary** (receiving, not
mountable). Exactly one node may be Primary at a time — that is what stops two
machines from writing to the same filesystem and destroying it.

Three ideas explain most of SDS's behaviour:

**Quorum.** A replica set decides by majority who is allowed to serve I/O. Two
diskful replicas cannot form a majority when one is lost, so SDS adds a third
member with no disk — a **tiebreaker** — that votes but stores nothing. Without
it, losing either node suspends I/O on both.

**Promotion is automatic, by drbd-reactor.** You do not usually run `primary`
yourself. You declare what should run with the resource (a mount, a virtual IP,
some services) and drbd-reactor promotes a node and starts them, moving the
whole set elsewhere if that node dies. See [High availability](#11-high-availability).

**Snapshots, replicas and backups are three different things**, and only one of
them is a backup:

| | Protects against | Does not protect against |
| --- | --- | --- |
| Replica | Losing a machine | `rm -rf` — it replicates |
| Snapshot | `rm -rf` | Losing the pool it lives in |
| Backup | Losing the whole cluster | Nothing else, if it is verified |

---

## 2. Talking to the cluster

```bash
sds-cli node list                          # against 127.0.0.1:3374
sds-cli -c 192.168.1.250:3374 node list    # against a remote controller
```

With Self-HA enabled the controller moves between nodes; point `-c` at the
floating VIP rather than at a node, so it never goes stale:

```bash
sds-cli ha self status        # prints the VIP
export SDS_CONTROLLER_ADDR=192.168.1.250:3374
```

If the cluster has RBAC or token auth on, supply a token with `--token`, the
`SDS_TOKEN` environment variable, or a file at `~/.sds/token`.

Two commands worth knowing before anything else:

```bash
sds-cli health-check          # can the controller reach every node, and is the stack installed
sds-cli resource status <name>  # everything about one resource: roles, disks, replication
```

---

## 3. Nodes

```bash
sds-cli node register --name orange1 --address 192.168.1.11
sds-cli node list
sds-cli node get orange1
```

`--address` is the management IP SDS uses for SSH. If replication should run
over a different network — a dedicated 10G link, say — name it separately:

```bash
sds-cli node register --name orange1 --address 192.168.1.11 \
    --replication-address 10.10.0.11
```

**Labels** describe where a node physically is. Auto-placement uses them to keep
replicas apart, or together:

```bash
sds-cli node label orange1 rack=A zone=east
sds-cli node label orange1 rack=          # trailing = deletes the label
```

**Renumbering** a node — its IP changed, or it moved subnet — is one command
once the node answers on the new address:

```bash
sds-cli node set-address orange1 192.168.1.21
sds-cli node set-address orange1 192.168.1.21 --replication-address 10.10.0.21
```

It checks the new address reaches the same machine, then moves the node in the
registry, rewrites its entry in every node's `/etc/hosts`, and rewrites the
DRBD config of each resource it takes part in; each reconnects on the new
address.

When several nodes changed address — a DHCP server handed every node a new
lease — renumber them in **one** command. One at a time cannot work then: each
node's resources would be rewritten through peers the controller still knows
only by their old addresses, and when two nodes trade addresses the configs
pass through a state with both on one.

```bash
sds-cli node set-address orange1=192.168.1.21 orange2=192.168.1.22 orange3=192.168.1.23
```

If every node moved, the controller cannot start at all: its database lives on
a DRBD resource whose peers can no longer find each other. Bring that one
resource back by hand first — map old to new in a single pass, since a plain
chain of `sed` substitutions breaks when two nodes trade addresses:

```bash
# on every node, with each node's old → new address
perl -pi -e 'my %m = ("192.168.1.11" => "192.168.1.21", "192.168.1.12" => "192.168.1.22",
                      "192.168.1.13" => "192.168.1.23");
             s/\b(\d+\.\d+\.\d+\.\d+)(?=:)/exists $m{$1} ? $m{$1} : $1/ge' /etc/drbd.d/sds-meta.res
drbdadm adjust sds-meta
# once the controller is up on its VIP:
sds-cli node set-address orange1=192.168.1.21 orange2=192.168.1.22 orange3=192.168.1.23
```

`resource repair <resource>` also writes the registry's addresses into a
resource's config, so a resource a renumbering could not reach (a node was
down) is fixed by repairing it afterwards.

**Draining** a node moves every Primary off it and refuses to place new ones
there — do this before maintenance, not after:

```bash
sds-cli node drain orange1
# ... reboot, replace a disk, upgrade ...
sds-cli node undrain orange1
```

Unregistering is for a node that is never coming back. Move its replicas off
first (see [remove-replica](#7-changing-a-resources-shape)).

---

## 4. Pools

A pool is the storage a node contributes. Three kinds:

```bash
sds-cli pool create --name data-pool --type lvm      --nodes orange1,orange2 --devices /dev/sdb
sds-cli pool create --name thin-pool --type lvm-thin --nodes orange1,orange2 --devices /dev/sdc
sds-cli pool create --name tank      --type zfs      --nodes orange1,orange2 --devices /dev/sdd
sds-cli pool list
```

**Choose thin unless you have a reason not to.** A thick (plain `lvm`) pool
cannot hold a useful snapshot history: LVM makes every thick snapshot reserve
its copy-on-write area up front, so a 10 GiB pool backing a 6 GiB volume fits
two snapshots whether or not anything ever changes. A thin snapshot costs only
the blocks that diverge.

Converting later is possible but is a rebuild, one node at a time:

```bash
sds-cli pool convert-thin --node orange1 --pool sds_data-pool
```

It destroys that node's copy and resyncs it in full from the peers. The resource
keeps serving throughout — the node goes diskless for the duration. It refuses
to start if the node holds a Primary, if a peer is not `UpToDate`, if a resync
is already running, or if this is one of only two diskful copies.

Growing a pool:

```bash
sds-cli pool add --pool data-pool --nodes orange1 --devices /dev/sde
```

---

## 5. Resources — your storage

The minimum:

```bash
sds-cli resource create --name db --size 100G --port 7000 --nodes orange1,orange2
```

That creates a 100 GiB replicated device on two nodes, adds a diskless
tiebreaker on a third if one is available, and brings it up. Each resource needs
its own **port** (7000+ by convention; it must be free on every node).

**Placement.** Omit `--nodes` and SDS picks by free space:

```bash
sds-cli resource create --name db --size 100G --port 7000 --replicas 2
sds-cli resource create --name db --size 100G --port 7000 --replicas 3 \
    --replicas-on-same zone --do-not-place-with web
```

`--replicas-on-same zone` keeps every replica in one zone (latency);
`--do-not-place-with web` keeps this resource off the nodes `web` uses
(anti-affinity). Placement is pool-aware: a volume only lands on a node that
actually hosts the requested pool.

**Profiles** group resources that should be alike. A resource created with
`--profile` — or attached later — is a member, and what is set on the profile
reaches every member:

```bash
sds-cli resource profile create --name db-tier --pool thin-pool --protocol C --replicas 2 ...
sds-cli resource create --name db --size 100G --port 7000 --profile db-tier
sds-cli resource set-profile legacy-db db-tier      # attach an existing resource
sds-cli resource list --profile db-tier             # the members
sds-cli resource profile get db-tier                # settings and members

# one change, every member: saved on the profile, applied to each resource
sds-cli resource profile set-options db-tier --drbd-options net/max-buffers=8000

# after raising --replicas, or for a member attached with fewer copies:
sds-cli resource profile adjust db-tier --dry-run   # what would change
sds-cli resource profile adjust db-tier             # add the missing replicas

sds-cli resource profile max-size db-tier           # largest volume a new member could get
```

`adjust` adds replicas where the profile asks for more, placed by its pool and
label constraints, and never removes one: a member with more copies than the
profile is reported and left alone. A profile with members cannot be deleted;
take them out first with `resource set-profile <resource> --none`.

**DRBD options** can be set at creation or changed later:

```bash
sds-cli resource create --name db ... --drbd-options on-no-quorum=suspend-io
sds-cli resource set-options db --drbd-options c-max-rate=200M
```

**Storage type** follows the pool automatically for LVM — a thin pool gets a
thin volume without your having to say so. ZFS is the exception and must be
named: `--storage-type zfs`.

---

## 6. Using a resource

A fresh resource is a raw block device. Put a filesystem on it and mount it:

```bash
sds-cli resource fs db 0 ext4              # <resource> <volume-id> <fstype>
sds-cli resource mount db 0 /mnt/db        # <resource> <volume-id> <mount-path>
```

Volume ids start at 0. A single-volume resource is always volume `0`.

For anything that should survive a node dying, do **not** mount it by hand —
declare it as HA instead and let drbd-reactor do the mounting. See
[High availability](#11-high-availability).

To see what you have:

```bash
sds-cli resource list
sds-cli resource status db      # roles, disk states, replication, per node
```

Read `status` like this: exactly one node should be `Primary`, every node's disk
should be `UpToDate`, and replication should be `Established`. Anything else is
covered in [When something is wrong](#20-when-something-is-wrong).

**Diskless clients** let a node mount a resource without storing a copy — it
reads and writes over the DRBD network:

```bash
sds-cli resource diskless attach db orange3
sds-cli resource mount db 0 /mnt/db
sds-cli resource diskless detach db orange3
```

Useful for a compute node that needs the data but has no disks to spare. It is
still bound by the one-Primary-at-a-time rule.

---

## 7. Changing a resource's shape

All of these run on a live resource.

```bash
# more capacity: <resource> <volume-id> <size>
sds-cli resource resize-volume db 0 200G

# another local replica
sds-cli resource add-replica db --node orange3

# take one out (the resource must keep at least two diskful copies)
sds-cli resource remove-replica db --node orange3

# more volumes in the same resource
sds-cli resource add-volume db --size 50G
sds-cli resource remove-volume db 1
```

**Removing a replica needs care.** DRBD records a peer's node-id in metadata, so
a node that was offline when you removed it can come back believing it is still
a member. If a node is unreachable during a removal, check that every node's
`/etc/drbd.d/<resource>.res` agrees afterwards:

```bash
# on each node
md5sum /etc/drbd.d/db.res
```

Mismatched files mean one node has a stale view — copy the correct one over and
`drbdadm adjust db`.

**Adopting** an existing DRBD resource that SDS did not create:

```bash
sds-cli resource adopt legacy-vol --nodes orange1,orange2
```

**Tiebreakers** can be moved if the node holding one is going away:

```bash
sds-cli ha set-tiebreaker db --node orange4
```

---

## 8. Snapshots

Snapshots live in the same pool as the resource. They are instant and cheap on a
thin pool, and they are **not a backup** — losing the pool loses both.

```bash
sds-cli resource snapshot create --resource db --name before-upgrade --pool thin-pool
sds-cli resource snapshot list --resource db
sds-cli resource snapshot restore --resource db --name before-upgrade
sds-cli resource snapshot delete --resource db --name before-upgrade
```

**Scheduled snapshots** with grandfather-father-son retention:

```bash
sds-cli resource snapshot schedule create --resource db \
    --cron "0 * * * *" \
    --keep-hourly 24 --keep-daily 7 --keep-weekly 4 --keep-monthly 6

sds-cli resource snapshot schedule list
sds-cli resource snapshot schedule delete --resource db
```

The cron field is standard 5-field syntax. Retention is applied after each run:
`--keep-hourly 24` keeps the most recent 24 hourly snapshots, and so on for each
tier. Deleting a schedule keeps the snapshots it already made.

Schedules live in the controller database and survive a restart or a failover —
the node that becomes active picks them up.

---

## 9. Backups — the only copy that survives losing the cluster

A backup is a full image of a resource shipped somewhere SDS cannot reach from
the cluster. Targets are S3-compatible object stores, SMB shares, or WebDAV.

**Define a target.** The secret is never a command-line flag — it would land in
your shell history:

```bash
export SDS_BACKUP_SECRET='...'
sds-cli backup target add --name offsite --kind s3 \
    --bucket sds-backups --endpoint https://s3.example.com --user AKIAEXAMPLE

sds-cli backup target add --name nas --kind smb \
    --host nas.lan --share backups --user backupuser --secret-file -

sds-cli backup target list        # secrets are never returned
```

An SMB host may carry a non-standard port (`--host nas.lan:4450`).

**Take and restore a backup:**

```bash
sds-cli backup create --resource db --target offsite
sds-cli backup list
sds-cli backup restore <backup-id> --node orange1
sds-cli backup delete <backup-id>
```

Know the limits before you build a policy on this:

- **Full images only.** No incremental, no compression — every backup transfers
  the whole volume. A 1 GiB volume takes roughly a minute and a half on a LAN.
- **No schedule.** Backups run when you run them, and accumulate until you
  delete them. Wrap `backup create` in cron if you need one.
- Only a backup listed as `completed` is restorable. `running` means it is still
  uploading; `failed` means it is not a usable copy. Completion is verified
  against the target's own reported size, not just the exit code.
- **Restore overwrites from byte zero.** It refuses when the resource is Primary
  anywhere, when a gateway exports it, or when the destination is smaller than
  the image.

---

## 10. Gateways — exporting to clients

A gateway turns a resource into something a non-SDS machine can mount: NFS,
iSCSI or NVMe-oF. It is a drbd-reactor promoter config, so it fails over with
the resource — clients keep talking to a floating service IP.

```bash
# NFS
sds-cli gateway nfs create --resource data --service-ip 192.168.1.200/24 \
    --export-path /data --allowed-ips 192.168.1.0/24

# iSCSI
sds-cli gateway iscsi create --resource blk \
    --iqn iqn.2026-01.com.example:sds.blk --service-ip 192.168.1.201/24

# NVMe-oF
sds-cli gateway nvme create --resource fast \
    --nqn nqn.2026-01.com.example:sds.fast --service-ip 192.168.1.202/24
```

Then reload drbd-reactor on the resource's nodes so it picks the config up:

```bash
ssh orange1 sudo systemctl reload drbd-reactor
ssh orange2 sudo systemctl reload drbd-reactor
```

Managing a live gateway:

```bash
sds-cli gateway list
sds-cli gateway status --resource data
sds-cli gateway stop  --resource data      # demote and stop, config kept
sds-cli gateway start --resource data
sds-cli gateway delete --resource data
```

Per-protocol details:

```bash
sds-cli gateway nfs export ...            # extra exports on an NFS gateway
sds-cli gateway iscsi lun ...             # LUNs
sds-cli gateway iscsi chap ...            # CHAP authentication
sds-cli gateway iscsi initiator ...       # initiator ACLs
sds-cli gateway nvme namespace ...        # namespaces
sds-cli gateway nvme host ...             # host allow-list
```

**What clients need.** Windows has a built-in iSCSI initiator and a limited
NFSv3 client; macOS has a built-in NFS client and no iSCSI initiator. There is
no SMB gateway. Pick the protocol by what the client can actually mount.

**Before you create an NFS gateway**, make sure `nfs-kernel-server` is installed
on the nodes. Creation does not check, so it reports success and prints mount
instructions for a gateway that will never start; the real error only appears in
`journalctl -u ocf.rs@nfsserver_<resource>`.

---

## 11. High availability

`ha create` declares what should run wherever the resource is Primary.
drbd-reactor then picks a node, mounts the filesystem, raises the VIP, starts the
services — and moves all of it if that node dies.

```bash
sds-cli ha create db \
    --vip 192.168.1.210/24 \
    --mount /var/lib/postgresql \
    --fstype ext4 \
    --services postgresql.service

sds-cli ha list
sds-cli ha status db
sds-cli ha delete db
```

Move it deliberately — for maintenance, or to test that failover works:

```bash
sds-cli ha evict db
```

**Test your failover before you need it.** Evicting is the polite path; pulling
power on the active node is the honest one.

### The controller's own HA

The controller is a single process. Self-HA puts it on the same machinery as
everything else: its database lives on a replicated resource, and a VIP follows
whichever node is running it.

```bash
sds-cli ha self enable --vip 192.168.1.250/24 --pool thin-pool
sds-cli ha self status
sds-cli ha self disable
```

Point clients at the VIP afterwards. Other services can be made to ride along —
`[self_ha] extra_services = ["sds-ai.service"]` in `controller.toml` starts and
stops the AI Copilot with the controller.

---

## 12. Cross-site replication (WAN DR)

WAN replication keeps an asynchronous replica at another site, over the public
internet, through a TCP proxy with mTLS. It is opt-in per resource because it
uses protocol A (asynchronous): the primary does not wait for the DR site.

```bash
sds-cli resource create --name db --size 100G --port 7000 \
    --nodes orange1,orange2 \
    --wan --dr-node aliyun1 --dr-endpoint dr.example.com
```

`--dr-endpoint` is an address or host name, without a port: the WAN port is
`--wan-port`, and 0 (the default) picks a random free port above 3000.
`--wan-egress-address` pins the outbound side to one interface when the primary
has several.

When the DR site's address changes, or the primary should dial out from
another interface:

```bash
sds-cli wan set-endpoint db --dr-endpoint dr2.example.com
sds-cli wan set-endpoint db --egress-address 203.0.113.20     # or --clear-egress
```

The tunnels are rebuilt on the new address at once. A new endpoint that does
not answer is refused and the old one kept — the old tunnel was working. For a
DR site whose firewall is not open yet, `--skip-check` saves it anyway; run
`wan repair` once it answers. Renumbering the DR node with `node set-address`
moves an endpoint that was that node's address by itself.

DR is manual on purpose — an automatic cross-site promotion during a network
partition is how you get two live copies:

```bash
sds-cli resource dr-failover db
```

Each primary-site node gets its own tunnel ("leg"). If a node is renumbered or
removed, its leg can be left behind:

```bash
sds-cli wan repair db --dry-run     # show the plan, touch nothing
sds-cli wan repair db
```

It converges, so running it on a healthy resource reports nothing to do. It
restarts tunnels, so use `--dry-run` first.

---

## 13. Storage tiering

Put an SSD in front of a thin pool, and every volume in the pool reads and
writes through it (lvmcache).

```bash
sds-cli pool add-cache --node orange1 --pool thin-pool --device /dev/nvme0n1
sds-cli pool remove-cache --node orange1 --pool thin-pool
```

The device is consumed whole and must be free — no filesystem signature, no
partitions in use, not already a PV.

**Writethrough is the default. `--mode writeback` must be asked for by name**,
because it puts the SSD in the durability path: a node that dies with a dirty
cache takes acknowledged writes with it, and the surviving replica may never
have seen them. `remove-cache` flushes first and reports failure if the flush
cannot be confirmed, rather than letting you pull a device with data on it.

The cache is per node. DRBD replicates volumes, not the block layer beneath
them, so caching one replica and not another is a legitimate end state.

Requires a thin pool: a thick pool has no single LV that every volume passes
through.

---

## 14. Encryption

`--encrypt` wraps each replica's backing volume in its own LUKS2 container, so
the stack is DRBD → LUKS → LVM.

```bash
sds-cli resource create --name secrets --size 50G --port 7010 \
    --nodes orange1,orange2 --encrypt
```

Understand exactly what this does and does not do:

- **At rest only.** DRBD sits above the crypt layer, so replication traffic
  between nodes is plaintext. Encrypt the network separately if you need that.
- **Each node generates and keeps its own key** under `/etc/sds/luks`, root-only.
  Nothing is sent anywhere and there is no central escrow. Lose a node's key and
  that replica is gone — the others are unaffected.
- **It cannot be enabled later.** Decide at creation.
- LVM pools only.

---

## 15. Alerts and notifications

Turn detection on in `controller.toml`:

```toml
[alert]
enabled = true
check_interval_sec = 60
check_nodes = true      # SSH-probe each node; produces node.unreachable
history_size = 500
```

Events: `resource.degraded`, `resource.failover`, `resource.no_primary`,
`resource.promoted`, `node.unreachable`, `wan.degraded`. Each carries a severity
(`info`/`warning`/`critical`) and a status — `firing` when a condition starts,
`resolved` when it clears — so a receiver can pair an alert with its recovery
instead of reading the recovery as a new fault.

```bash
sds-cli event list
sds-cli event watch --min-severity critical
sds-cli event watch --type resource.failover --json
```

### Sending them somewhere

A chat service will not accept an arbitrary JSON document, so a channel has a
**kind**. Worse, Feishu, WeCom and DingTalk report a refusal *inside an HTTP
200*, so a misconfigured channel looks like it is working until an outage passes
unnoticed.

```bash
sds-cli channel add --name oncall --kind feishu \
    --url https://open.feishu.cn/open-apis/bot/v2/hook/xxxx --min-severity warning

sds-cli channel add --name pager --kind slack \
    --url https://hooks.slack.com/services/T00/B00/xxxx --min-severity critical

export SDS_NOTIFY_SECRET=SECxxxx        # DingTalk 加签, never a flag
sds-cli channel add --name ops --kind dingtalk \
    --url 'https://oapi.dingtalk.com/robot/send?access_token=xxxx'

sds-cli channel test oncall             # reports what the service itself said
sds-cli channel list
```

Kinds: `generic` (the event JSON unchanged, for a receiver you wrote), `feishu`,
`slack`, `wecom`, `dingtalk`. A well-known bot URL saved with the wrong kind is
refused up front rather than at delivery time.

**Always run `channel test` after adding one.** It is the only thing that
distinguishes a working channel from a silent one.

Channels live in the controller database, so adding or muting one takes effect
immediately — no restart. `--muted` stores a channel without delivering to it,
which is how you silence a noisy pager during an incident without having to find
the bot URL again afterwards.

The same events are also readable at `GET /v1/events`, streamable at
`/v1/events/watch`, and pushed to the web UI's bell over `/v1/events/stream`.

---

## 16. Access control

```bash
sds-cli rbac policies      # effective roles and assignments (admin only)
```

Roles are `admin`, `operator` and `viewer`. Operations are classified by object
(pool, resource, gateway, snapshot, backup, node, ha, system) and action (read,
write). An operator can run the cluster but not change system-level
configuration; a viewer can only look.

Tokens come from `--token`, `SDS_TOKEN`, `~/.sds/token` or `/etc/sds/token`.

---

## 17. Kubernetes

SDS ships a CSI driver. Volumes are DRBD resources; a pod moving between nodes
gets its storage promoted on the new one.

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: sds
provisioner: csi.sds.io
parameters:
  pool: thin-pool
  replicas: "2"
```

`allowRemoteVolumeAccess` opts a volume into diskless attachment, so a pod can
run on a node that holds no replica.

See the CSI section of [deployment-guide.md](deployment-guide.md) for
installation and the RBAC the driver needs.

---

## 18. The AI Copilot

`sds-ai` is an optional service that answers questions about the cluster in the
web UI's Copilot sidebar. It reaches the cluster through read-only MCP tools
only — it can inspect everything and change nothing. When a change is warranted
it proposes one, and the operator approves it in the UI before the controller
carries it out.

Two things determine how useful it is:

- **Its knowledge base.** Check what it has: `GET /ai/kb/list`. A near-empty one
  makes it fall back on the model's own memory, which for a specific question
  about your cluster is how you get a confident wrong answer. Feed it with
  `POST /ai/kb/ingest` (a directory) or `/ai/kb/doc` (one document).
- **The embedder matching the index.** `SDS_AI_EMB_DIM` must equal the width the
  index was built at. Change the embedder to one of a different width and every
  search silently returns nothing — no error, just no results. `/ai/kb/list`
  reports the width read back from the index, which is how you check.

Send the same `session_id` across turns and it follows a conversation; omit it
and every question starts from nothing.

---

## 19. Routine operations

**Before a node reboot**

```bash
sds-cli node drain orange1
# ... work ...
sds-cli node undrain orange1
sds-cli resource status <each affected resource>   # wait for UpToDate everywhere
```

**Growing a volume**

```bash
sds-cli resource resize-volume db 0 200G
# then grow the filesystem on the Primary
ssh <primary> sudo resize2fs /dev/drbd<minor>
```

**Replacing a disk**

Drain the node, remove its replica, replace the disk, recreate the pool, add the
replica back, and wait for the resync to finish before touching the next node.

**Upgrading the controller**

```bash
make build
scp bin/sds-controller <node>:/tmp/
ssh <node> "sudo systemctl stop sds-controller && \
  sudo cp /tmp/sds-controller /usr/local/bin/ && \
  sudo systemctl start sds-controller"
```

With Self-HA on, upgrade the standby nodes first, then evict the controller onto
one of them, then upgrade the last node.

**Checking a cluster you have not looked at in a while**

```bash
sds-cli health-check
sds-cli resource list
sds-cli event list --min-severity warning
sds-cli backup list          # is anything still 'running' from a dead controller?
sds-cli channel test <each>  # would you actually be told?
```

---

## 20. When something is wrong

Start here:

```bash
sds-cli resource status <name>
sds-cli event list --resource <name>
ssh <node> sudo drbdadm status <name>
ssh <node> sudo journalctl -u drbd-reactor -n 50
```

### Reading `resource status`

| What you see | What it means |
| --- | --- |
| `role:Primary` on exactly one node | Normal. |
| `role:Secondary` everywhere | Nobody is serving I/O. Normal for an idle resource; a problem if something should be mounted. |
| `disk:UpToDate` | This replica has every byte. |
| `disk:Inconsistent` | Mid-resync, or never synced. Wait. |
| `disk:Outdated` | This copy is known stale — it will not be promoted, which is correct. |
| `disk:Diskless` | No local copy. Expected on a tiebreaker or a diskless client; a fault anywhere else. |
| `peer-disk:DUnknown` | Cannot see that peer at all. Network, or the peer is down. |
| `replication:Established` | Healthy. |
| `replication:StandAlone` | The connection was dropped, usually after a split brain. It will not reconnect by itself. |
| `quorum:no` | This replica may not serve I/O. See below. |

### Common situations

**I/O suspended on a resource, `quorum:no`.** The replica set cannot form a
majority. Usually a two-diskful resource with no tiebreaker after losing one
node. Register a third node so a tiebreaker can be added. As an emergency
measure on the surviving diskful node only:

```bash
sudo drbdsetup resource-options <res> --quorum=1     # runtime only, not persisted
# ... once the peer is back and UpToDate ...
sudo drbdsetup resource-options <res> --quorum=majority
```

Change it on one node only. The peers keep `majority`, so a peer starting alone
still cannot be promoted — which is what stops this from becoming a split brain.

**A resource is stuck `Inconsistent` after creation.** A fresh DRBD device needs
an initial forced promotion to declare one copy authoritative. Recent
controllers do this automatically; on an older one, promote once by hand.

**`StandAlone` after a split brain.** Two copies diverged. Decide which one is
authoritative — SDS will not guess — then discard the other's changes and
reconnect it. Anything written to the discarded side is lost, so look at both
before choosing.

**A gateway will not start.** Look at the OCF agent, not at drbd-reactor:

```bash
ssh <node> sudo drbd-reactorctl status
ssh <node> sudo journalctl -u "ocf.rs@*<resource>*" -n 50
```

The usual causes are a missing package (`nfs-kernel-server`, `targetcli-fb`, the
`nvmet-tcp` module) and a service IP already in use.

**A node is unreachable but the machine is up.** SDS reaches nodes over SSH as
root. After a node is rebuilt its host key changes and every operation fails
with an empty error; clear the stale key and restart the controller.

**Nothing is alerting.** Check three things in order: `[alert] enabled = true`,
at least one enabled channel in `sds-cli channel list`, and `sds-cli channel
test` on each of them.

### Getting more detail

```bash
sds-cli event watch                # follow live
sds-cli rbac policies              # "permission denied" that should not be
journalctl -u sds-controller -f    # on the controller node
```

The [deployment guide's gotchas table](deployment-guide.md#13-top-gotchas-learned-the-hard-way)
collects the failures that cost the most time to diagnose the first time.
