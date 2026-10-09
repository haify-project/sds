from urllib.parse import quote

from django.utils.translation import gettext_lazy as _

from horizon import tabs

from openstack_dashboard import api

from haify_cinder.client import HaifyError
from haify_horizon import conf
from haify_horizon import data


class ReplicationTab(tabs.Tab):
    """Where a Haify volume's replicas are, which is Primary, and how they
    are syncing. Shown on the admin volume details of Haify volumes only."""

    name = _("Replication")
    slug = "haify"
    template_name = "haify/_volume_tab.html"
    preload = False

    def allowed(self, request):
        return conf.on_haify(self.tab_group.kwargs["volume"])

    def get_context_data(self, request):
        volume = self.tab_group.kwargs["volume"]
        name = conf.prefix() + volume.id
        context = {"resource_name": name, "error": None, "ui_link": ""}
        if conf.ui_url():
            context["ui_link"] = f"{conf.ui_url()}/resources?q={quote(name)}"
        client = conf.client()
        try:
            res = client.resource(name)
            status = client.status(name)
            snapshots = client.snapshots(name)
        except HaifyError as e:
            context["error"] = str(e)
            return context
        try:
            found = api.cinder.volume_snapshot_list(
                request, search_opts={"volume_id": volume.id, "all_tenants": True})
        except Exception:
            found = []
        members = data.replicas(res, status)
        state, detail = data.health(members, status)
        context.update(
            resource=res,
            members=members,
            primary=data.primary_of(members),
            health=state,
            health_detail=detail,
            snapshots=data.snapshot_rows(snapshots, found),
            labels=sorted((res.get("labels") or {}).items()),
        )
        return context
