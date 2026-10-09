from django.urls import reverse
from django.utils.html import format_html, format_html_join
from django.utils.translation import gettext_lazy as _

from horizon import tables

# Bootstrap label classes for a member's state.
HEALTH_CLASS = {"healthy": "success", "syncing": "info", "degraded": "danger", "unknown": "default"}


def member_class(m):
    if m["kind"] == "replica" and m["disk"] != "UpToDate":
        return "warning" if m["sync"] < 100 else "danger"
    if m["role"] == "Primary":
        return "primary"
    return "default"


def member_text(m):
    text = m["node"]
    if m["kind"] != "replica":
        text += f" ({m['kind']})"
    if m["kind"] == "replica" and m["disk"] != "UpToDate":
        text += f" {m['disk']}" + (f" {m['sync']:.0f}%" if m["sync"] < 100 else "")
    return text


def render_members(row):
    return format_html_join(
        " ", '<span class="label label-{}" title="{} · {}">{}</span>',
        ((member_class(m), m["role"], m["disk"], member_text(m)) for m in row.members))


def render_health(row):
    return format_html('<span class="label label-{}" title="{}">{}</span>',
                       HEALTH_CLASS.get(row.health, "default"), row.health_detail, row.health)


def render_volume(row):
    if not row.volume_id:
        return format_html("<em>{}</em>", _("not in Cinder"))
    url = reverse("horizon:admin:volumes:detail", args=[row.volume_id])
    return format_html('<a href="{}">{}</a>', url, row.volume_name)


def render_attached(row):
    if not row.attached:
        return "-"
    return format_html_join(
        ", ", '<a href="{}">{}</a> on {}',
        ((reverse("horizon:admin:instances:detail", args=[a["id"]]), a["name"], a["host"] or "?")
         for a in row.attached))


def render_primary(row):
    return ", ".join(row.primary) or "-"


class VolumeFilter(tables.FilterAction):
    def filter(self, table, rows, query):
        q = query.lower()
        return [r for r in rows
                if q in r.volume_name.lower() or q in r.resource.lower()
                or any(q in m["node"].lower() for m in r.members)]


class VolumesTable(tables.DataTable):
    volume = tables.Column(render_volume, verbose_name=_("Volume"))
    status = tables.Column("volume_status", verbose_name=_("Status"))
    size = tables.Column("size_gb", verbose_name=_("Size (GiB)"))
    attached = tables.Column(render_attached, verbose_name=_("Attached To"))
    primary = tables.Column(render_primary, verbose_name=_("Primary On"))
    members = tables.Column(render_members, verbose_name=_("Replicas"))
    health = tables.Column(render_health, verbose_name=_("Health"))

    class Meta(object):
        name = "volumes"
        verbose_name = _("Volumes")
        table_actions = (VolumeFilter,)


def render_room(row):
    if not row.total_gb:
        return "-"
    return f"{row.free_gb} / {row.total_gb} GiB free ({row.used_percent}% used)"


class NodesTable(tables.DataTable):
    name = tables.Column("name", verbose_name=_("Node"))
    address = tables.Column("address", verbose_name=_("Address"))
    state = tables.Column("state", verbose_name=_("State"))
    room = tables.Column(render_room, verbose_name=_("Pool"))
    replicas = tables.Column("replicas", verbose_name=_("Replicas"))
    tiebreakers = tables.Column("tiebreakers", verbose_name=_("Tiebreaker for"))
    clients = tables.Column("clients", verbose_name=_("Diskless clients"))
    primaries = tables.Column("primaries", verbose_name=_("Primary for"))

    class Meta(object):
        name = "nodes"
        verbose_name = _("Nodes")
