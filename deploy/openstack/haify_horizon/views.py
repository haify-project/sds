from django.utils.translation import gettext_lazy as _

from horizon import exceptions
from horizon import tables

from openstack_dashboard import api

from haify_cinder.client import HaifyError
from haify_horizon import conf
from haify_horizon import data
from haify_horizon import tables as haify_tables


class IndexView(tables.MultiTableView):
    table_classes = (haify_tables.VolumesTable, haify_tables.NodesTable)
    template_name = "haify/index.html"
    page_title = _("Haify")

    def _load(self):
        if hasattr(self, "_haify"):
            return self._haify
        client = conf.client()
        self._haify = {"resources": [], "statuses": {}, "nodes": [], "pools": [], "error": None}
        try:
            resources = client.resources()
            mine = data.cinder_resources(resources, conf.prefix())
            self._haify.update(
                resources=resources,
                statuses=data.statuses(client, sorted(mine)),
                nodes=client.request("GET", "/v1/nodes").get("nodes") or [],
                pools=client.pools(),
            )
        except HaifyError as e:
            self._haify["error"] = str(e)
            exceptions.handle(self.request, _("Unable to reach the Haify controller: %s") % e)
        return self._haify

    def get_volumes_data(self):
        h = self._load()
        volumes, servers = [], {}
        try:
            volumes = api.cinder.volume_list(self.request, search_opts={"all_tenants": True})
        except Exception:
            exceptions.handle(self.request, _("Unable to retrieve volumes."))
        try:
            found, _more = api.nova.server_list(self.request, search_opts={"all_tenants": True})
            servers = {s.id: s.name for s in found}
        except Exception:
            exceptions.handle(self.request, _("Unable to retrieve instances."))
        return data.volume_rows(h["resources"], h["statuses"], volumes, servers, conf.prefix())

    def get_nodes_data(self):
        h = self._load()
        mine = data.cinder_resources(h["resources"], conf.prefix())
        pairs = [(r, h["statuses"].get(n)) for n, r in mine.items()]
        return data.node_rows(h["nodes"], h["pools"], pairs, conf.setting("HAIFY_POOL", ""))

    def get_context_data(self, **kwargs):
        context = super().get_context_data(**kwargs)
        h = self._load()
        rows = self.get_tables()["volumes"].data or []
        context["haify_error"] = h["error"]
        context["haify_ui"] = conf.ui_url()
        context["haify_counts"] = {
            state: sum(1 for r in rows if r.health == state)
            for state in ("healthy", "syncing", "degraded", "unknown")
        }
        return context
