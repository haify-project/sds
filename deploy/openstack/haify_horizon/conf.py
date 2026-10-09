"""The plugin's settings, from Horizon's local_settings.py.

    HAIFY_CONTROLLER = "192.168.1.10,192.168.1.11"   # REST, port 3375 by default
    HAIFY_TOKEN_FILE = "/etc/haify/horizon.token"     # with [auth] or [rbac]
    HAIFY_RESOURCE_PREFIX = "cinder-"                 # as haify_resource_prefix
    HAIFY_POOL = "pool0"                              # as haify_pool, for capacity
    HAIFY_BACKENDS = ["haify"]                        # Cinder backend names
    HAIFY_UI_URL = "http://192.168.1.10:3376"         # Haify's web UI, for links
"""

from django.conf import settings

from haify_cinder.client import Client


def setting(name, default=None):
    return getattr(settings, name, default)


def prefix():
    return setting("HAIFY_RESOURCE_PREFIX", "cinder-")


def ui_url():
    return (setting("HAIFY_UI_URL", "") or "").rstrip("/")


def backends():
    return set(setting("HAIFY_BACKENDS", ["haify"]))


def client():
    token = None
    path = setting("HAIFY_TOKEN_FILE", "")
    if path:
        with open(path) as f:
            token = f.read().strip() or None
    return Client(setting("HAIFY_CONTROLLER", ""), token=token, timeout=15)


def on_haify(volume):
    """Whether Cinder placed volume on a Haify backend. The host attribute
    reads "<host>@<backend>#<pool>", and only admins are shown it."""
    host = getattr(volume, "os-vol-host-attr:host", "") or ""
    if "@" not in host:
        return False
    return host.split("@", 1)[1].split("#", 1)[0] in backends()
