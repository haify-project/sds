"""The Cinder-facing layer, against stand-ins for the few Cinder and oslo
modules it imports, so it runs where Cinder is not installed."""

import os
import sys
import types
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))


def stub_cinder():
    if "cinder" in sys.modules and not getattr(sys.modules["cinder"], "_stub", False):
        return  # the real one is installed

    class Opt:
        def __init__(self, name, default=None, **kw):
            self.name, self.default = name, default

    class Conf:
        def register_opts(self, opts, group=None):
            pass

    cfg = types.SimpleNamespace(ListOpt=Opt, StrOpt=Opt, IntOpt=Opt, DictOpt=Opt, CONF=Conf())
    log = types.SimpleNamespace(getLogger=lambda name: types.SimpleNamespace(
        info=lambda *a: None, warning=lambda *a: None, error=lambda *a: None))

    class VolumeBackendAPIException(Exception):
        def __init__(self, data=""):
            super().__init__(data)

    class InvalidConfigurationValue(Exception):
        def __init__(self, **kw):
            super().__init__(kw)

    class VolumeDriver:
        def __init__(self, *args, configuration=None, **kwargs):
            self.configuration = configuration

    mods = {
        "oslo_config": types.SimpleNamespace(cfg=cfg),
        "oslo_log": types.SimpleNamespace(log=log),
        "cinder": types.SimpleNamespace(_stub=True),
        "cinder.exception": types.SimpleNamespace(VolumeBackendAPIException=VolumeBackendAPIException,
                                                  InvalidConfigurationValue=InvalidConfigurationValue),
        "cinder.interface": types.SimpleNamespace(volumedriver=lambda cls: cls),
        "cinder.volume": types.SimpleNamespace(),
        "cinder.volume.configuration": types.SimpleNamespace(SHARED_CONF_GROUP="backend_defaults"),
        "cinder.volume.driver": types.SimpleNamespace(VolumeDriver=VolumeDriver),
    }
    for name, mod in mods.items():
        sys.modules[name] = mod
    sys.modules["cinder"].exception = mods["cinder.exception"]
    sys.modules["cinder"].interface = mods["cinder.interface"]
    sys.modules["cinder.volume"].configuration = mods["cinder.volume.configuration"]
    sys.modules["cinder.volume"].driver = mods["cinder.volume.driver"]


stub_cinder()

from fakes import FakeClient, HaifyError  # noqa: E402
from haify_cinder import driver as haify_driver  # noqa: E402
from haify_cinder.backend import Backend, Settings  # noqa: E402

RES = "cinder-v1"


class Configuration:
    def __init__(self):
        self.haify_controller = ["ctl"]

    def append_config_values(self, opts):
        pass

    def safe_get(self, key):
        return {"volume_backend_name": "haify-a"}.get(key)


def attachment(id_, host=None, connector=None):
    return types.SimpleNamespace(id=id_, attached_host=host, connector=connector)


def make_driver():
    d = haify_driver.HaifyDriver(configuration=Configuration())
    c = FakeClient()
    d.backend = Backend(c, Settings(controller=["ctl"]))
    return d, c


class Driver(unittest.TestCase):
    def test_migration_source_is_seen_by_the_destination(self):
        d, c = make_driver()
        c.res[RES] = {"name": RES, "nodes": ["n1", "n2"]}
        c.roles[RES] = {"n1": "Primary"}
        vol = types.SimpleNamespace(id="v1", volume_attachment=[
            attachment("a-src", host="n1"), attachment("a-dst", connector={"host": "n2"})])
        info = d.initialize_connection(vol, {"host": "n2"})
        self.assertEqual(info["data"]["device_path"], f"/dev/drbd/by-res/{RES}/0")
        self.assertIn(("dual_primary", RES, True, ["n1", "n2"]), c.calls)

    def test_terminate_leaves_out_its_own_attachment(self):
        d, c = make_driver()
        c.res[RES] = {"name": RES, "nodes": ["n1"], "disklessClients": ["c9"]}
        a = attachment("a1", host="c9")
        vol = types.SimpleNamespace(id="v1", volume_attachment=[a])
        d.terminate_connection(vol, {"host": "c9"}, attachment=a)
        self.assertIn(("detach_client", RES, "c9"), c.calls)

    def test_force_detach_touches_nothing(self):
        d, c = make_driver()
        d.terminate_connection(types.SimpleNamespace(id="v1"), None)
        self.assertEqual(c.calls, [])

    def test_errors_become_backend_errors(self):
        d, c = make_driver()
        c.fail["create_resource"] = HaifyError("pool full")
        with self.assertRaisesRegex(sys.modules["cinder.exception"].VolumeBackendAPIException, "pool full"):
            d.create_volume(types.SimpleNamespace(id="v1", size=1))

    def test_stats(self):
        d, c = make_driver()
        c.pool_list = [{"name": "haify_p", "thinPoolLv": "t", "thinSizeBytes": str(10 * 1024 ** 3),
                        "thinDataPercent": 60}] * 2
        d._update_volume_stats()
        s = d._stats
        self.assertEqual((s["total_capacity_gb"], s["free_capacity_gb"]), (10.0, 4.0))
        self.assertEqual(s["volume_backend_name"], "haify-a")
        self.assertTrue(s["thin_provisioning_support"])
        c.fail["pools"] = HaifyError("down")
        d._update_volume_stats()
        self.assertEqual(d._stats["backend_state"], "down")


if __name__ == "__main__":
    unittest.main()
