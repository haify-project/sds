"""What the Horizon panel shows, built from plain data.

The controller describes resources and nodes; Cinder and Nova describe
volumes and instances. These functions put them side by side without
importing Horizon, so the rules are tested without a dashboard.
"""

from concurrent.futures import ThreadPoolExecutor

from haify_cinder.backend import MANAGED_BY, MANAGED_BY_LABEL, num, pool_space
from haify_cinder.client import HaifyError

GiB = 1024 ** 3
STATUS_WORKERS = 8

# A replica that is a full, current copy.
GOOD_DISK = "UpToDate"


class Row:
    """One table row. Horizon tables read attributes and need an id."""

    def __init__(self, **kw):
        self.__dict__.update(kw)

    def __repr__(self):
        return f"Row({self.__dict__})"


def cinder_resources(resources, prefix):
    """The resources the Cinder driver created, by name."""
    out = {}
    for r in resources:
        name = r.get("name", "")
        if (r.get("labels") or {}).get(MANAGED_BY_LABEL) == MANAGED_BY or (prefix and name.startswith(prefix)):
            out[name] = r
    return out


def statuses(client, names, workers=STATUS_WORKERS):
    """Each resource's live status, fetched side by side; None for one the
    controller could not report."""

    def one(name):
        try:
            return name, client.status(name)
        except HaifyError:
            return name, None

    if not names:
        return {}
    with ThreadPoolExecutor(max_workers=min(workers, len(names))) as pool:
        return dict(pool.map(one, names))


def replicas(resource, status):
    """The resource's members, one dict each: node, kind (replica, tiebreaker,
    client), role, disk state and sync percentage."""
    states = {}
    for host, st in ((status or {}).get("nodeStates") or {}).items():
        states[st.get("node") or host] = st
    members = [(n, "replica") for n in resource.get("nodes") or []]
    members += [(n, "tiebreaker") for n in resource.get("disklessNodes") or []]
    members += [(n, "client") for n in resource.get("disklessClients") or []]
    out = []
    for node, kind in members:
        st = states.get(node, {})
        disk = st.get("diskState") or ("Diskless" if kind != "replica" else "Unknown")
        # A peer the answering node is not connected to has no known disk
        # (DRBD says DUnknown): say what is wrong instead.
        if disk in ("DUnknown", "Unknown") or st.get("connection") in ("Connecting", "StandAlone"):
            disk = "disconnected" if kind == "replica" else disk
        out.append({
            "node": node,
            "kind": kind,
            "role": st.get("role") or "Unknown",
            "disk": disk,
            "connection": st.get("connection") or "",
            "sync": num(st.get("syncPercent", 100)),
        })
    return out


def health(members, status):
    """(state, detail): healthy, syncing, degraded or unknown."""
    if status is None:
        return "unknown", "the controller did not report it"
    copies = [m for m in members if m["kind"] == "replica"]
    syncing = [m for m in copies if m["disk"] in ("Inconsistent", "SyncTarget") or m["sync"] < 100]
    bad = [m for m in copies if m["disk"] != GOOD_DISK and m not in syncing]
    if bad:
        return "degraded", ", ".join(f"{m['node']} {m['disk']}" for m in bad)
    if syncing:
        low = min(m["sync"] for m in syncing)
        return "syncing", f"{low:.0f}%"
    return "healthy", f"{len(copies)} up-to-date copies"


def primary_of(members):
    return [m["node"] for m in members if m["role"] == "Primary"]


def volume_rows(resources, status_by_name, volumes, server_names, prefix):
    """A row per Cinder volume on Haify, plus one per resource whose volume
    Cinder no longer knows (left behind by a failed delete)."""
    by_id = {getattr(v, "id", None): v for v in volumes}
    rows = []
    for name, res in sorted(cinder_resources(resources, prefix).items()):
        status = status_by_name.get(name)
        members = replicas(res, status)
        state, detail = health(members, status)
        vol_id = name[len(prefix):] if prefix and name.startswith(prefix) else ""
        vol = by_id.get(vol_id)
        attached = []
        for a in getattr(vol, "attachments", None) or []:
            sid = a.get("server_id")
            if sid:
                attached.append({"id": sid, "name": server_names.get(sid) or sid, "host": a.get("host_name")})
        size = 0
        for v in res.get("volumes") or []:
            size += num(v.get("sizeBytes")) or num(v.get("sizeGb")) * GiB
        rows.append(Row(
            id=name,
            resource=name,
            volume_id=vol_id if vol else "",
            volume_name=(getattr(vol, "name", "") or vol_id) if vol else "",
            volume_status=getattr(vol, "status", "") if vol else "not in Cinder",
            size_gb=round(size / GiB, 2),
            attached=attached,
            primary=primary_of(members),
            members=members,
            health=state,
            health_detail=detail,
            quorum_risk=bool(res.get("quorumRisk")),
            fault_domain_risk=res.get("faultDomainRisk") or "",
        ))
    return rows


def snapshot_rows(names, cinder_snapshots):
    """Haify's snapshots of a volume, named as Cinder names them. One Cinder
    does not know (a clone's temporary snapshot, or one taken in Haify) is
    listed with Cinder's name empty."""
    by_id = {getattr(s, "id", None): s for s in cinder_snapshots}
    out = []
    for name in names:
        s = by_id.get(name)
        out.append({"haify": name, "id": name if s else "", "name": (getattr(s, "name", "") or name) if s else ""})
    return out


def node_rows(nodes, pools, resources, pool_name=""):
    """A row per Haify node: its state, the pool's room, and how many
    replicas and Primaries of Cinder volumes it holds."""
    by_address = {n.get("address"): n.get("name") for n in nodes}
    room = {}
    for p in pools:
        if pool_name and p.get("name") not in (pool_name, "haify_" + pool_name):
            continue
        node = by_address.get(p.get("node"), p.get("node"))
        _, free, total = pool_space(p)
        f, t = room.get(node, (0.0, 0.0))
        room[node] = (f + free, t + total)
    counts = {}
    for res, status in resources:
        for m in replicas(res, status):
            c = counts.setdefault(m["node"], {"replicas": 0, "tiebreakers": 0, "clients": 0, "primaries": 0})
            c[{"replica": "replicas", "tiebreaker": "tiebreakers", "client": "clients"}[m["kind"]]] += 1
            if m["role"] == "Primary":
                c["primaries"] += 1
    rows = []
    for n in sorted(nodes, key=lambda n: n.get("name", "")):
        name = n.get("name", "")
        free, total = room.get(name, (0.0, 0.0))
        c = counts.get(name, {"replicas": 0, "tiebreakers": 0, "clients": 0, "primaries": 0})
        rows.append(Row(
            id=name,
            name=name,
            address=n.get("address", ""),
            state=n.get("state", ""),
            free_gb=round(free / GiB, 1),
            total_gb=round(total / GiB, 1),
            used_percent=round(100 * (1 - free / total)) if total else None,
            replicas=c["replicas"],
            tiebreakers=c["tiebreakers"],
            clients=c["clients"],
            primaries=c["primaries"],
        ))
    return rows
