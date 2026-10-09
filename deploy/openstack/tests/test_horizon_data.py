import os
import sys
import types
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))

from haify_cinder.client import HaifyError  # noqa: E402
from haify_horizon import data  # noqa: E402

GiB = 1024 ** 3


def res(name, nodes=("n1", "n2"), tb=("n3",), clients=(), labels=None):
    return {"name": name, "nodes": list(nodes), "disklessNodes": list(tb), "disklessClients": list(clients),
            "labels": labels if labels is not None else {"haify.openstack/managed-by": "cinder"},
            "volumes": [{"sizeBytes": str(2 * GiB)}]}


def status(**roles):
    """status(n1=("Primary", "UpToDate", 100), ...)"""
    return {"nodeStates": {f"host-{n}": {"node": n, "role": r, "diskState": d, "syncPercent": s}
                           for n, (r, d, s) in roles.items()}}


class Members(unittest.TestCase):
    def test_kinds_and_states(self):
        m = data.replicas(res("cinder-a", clients=("c9",)), status(
            n1=("Primary", "UpToDate", 100), n2=("Secondary", "UpToDate", 100), n3=("Secondary", "Diskless", 100)))
        self.assertEqual([(x["node"], x["kind"], x["role"]) for x in m],
                         [("n1", "replica", "Primary"), ("n2", "replica", "Secondary"),
                          ("n3", "tiebreaker", "Secondary"), ("c9", "client", "Unknown")])
        self.assertEqual(data.primary_of(m), ["n1"])

    def test_health(self):
        r = res("cinder-a")
        ok = status(n1=("Primary", "UpToDate", 100), n2=("Secondary", "UpToDate", 100))
        self.assertEqual(data.health(data.replicas(r, ok), ok)[0], "healthy")
        sync = status(n1=("Primary", "UpToDate", 100), n2=("Secondary", "Inconsistent", 42.5))
        self.assertEqual(data.health(data.replicas(r, sync), sync), ("syncing", "42%"))
        lost = status(n1=("Primary", "UpToDate", 100))
        self.assertEqual(data.health(data.replicas(r, lost), lost), ("degraded", "n2 disconnected"))
        down = status(n1=("Primary", "UpToDate", 100), n2=("Unknown", "DUnknown", 100))
        self.assertEqual(data.health(data.replicas(r, down), down), ("degraded", "n2 disconnected"))
        self.assertEqual(data.health(data.replicas(r, None), None)[0], "unknown")


class Volumes(unittest.TestCase):
    def test_rows(self):
        resources = [res("cinder-v1"), res("cinder-orphan"), res("app-db", labels={})]
        st = {"cinder-v1": status(n1=("Primary", "UpToDate", 100), n2=("Secondary", "UpToDate", 100))}
        vol = types.SimpleNamespace(id="v1", name="data", status="in-use",
                                    attachments=[{"server_id": "s1", "host_name": "n1"}])
        rows = data.volume_rows(resources, st, [vol], {"s1": "web1"}, "cinder-")
        self.assertEqual([r.resource for r in rows], ["cinder-orphan", "cinder-v1"], "only Cinder's, sorted")
        v1 = rows[1]
        self.assertEqual((v1.volume_name, v1.volume_status, v1.size_gb, v1.primary, v1.health),
                         ("data", "in-use", 2.0, ["n1"], "healthy"))
        self.assertEqual(v1.attached, [{"id": "s1", "name": "web1", "host": "n1"}])
        orphan = rows[0]
        self.assertEqual((orphan.volume_id, orphan.volume_status, orphan.health),
                         ("", "not in Cinder", "unknown"))

    def test_statuses_tolerate_failures(self):
        class C:
            def status(self, name):
                if name == "bad":
                    raise HaifyError("timeout")
                return {"name": name}
        self.assertEqual(data.statuses(C(), ["ok", "bad"]), {"ok": {"name": "ok"}, "bad": None})
        self.assertEqual(data.statuses(C(), []), {})


class Nodes(unittest.TestCase):
    def test_rows(self):
        nodes = [{"name": "n2", "address": "10.0.0.2", "state": "online"},
                 {"name": "n1", "address": "10.0.0.1", "state": "online"}]
        pools = [{"name": "haify_p", "node": "10.0.0.1", "capacityBytes": str(16 * GiB),
                  "availableBytes": str(12 * GiB), "thin": True},
                 {"name": "haify_other", "node": "10.0.0.1", "capacityBytes": str(100 * GiB),
                  "availableBytes": str(100 * GiB)}]
        pairs = [(res("cinder-a", clients=("n2",), nodes=("n1",)), status(n1=("Primary", "UpToDate", 100)))]
        rows = data.node_rows(nodes, pools, pairs, "p")
        self.assertEqual([r.name for r in rows], ["n1", "n2"])
        n1, n2 = rows
        self.assertEqual((n1.free_gb, n1.total_gb, n1.used_percent, n1.replicas, n1.primaries), (12.0, 16.0, 25, 1, 1))
        self.assertEqual((n2.total_gb, n2.used_percent, n2.clients), (0.0, None, 1))

    def test_snapshots_named_as_cinder_names_them(self):
        rows = data.snapshot_rows(["s-1", "clone-ab"], [types.SimpleNamespace(id="s-1", name="before upgrade")])
        self.assertEqual(rows, [{"haify": "s-1", "id": "s-1", "name": "before upgrade"},
                                {"haify": "clone-ab", "id": "", "name": ""}])


if __name__ == "__main__":
    unittest.main()
