from django.utils.translation import gettext_lazy as _

import horizon


class Haify(horizon.Panel):
    name = _("Haify")
    slug = "haify"
    # As the admin Volumes panel: Cinder under any of the names it is
    # registered by, and an admin.
    permissions = (
        ("openstack.services.block-storage",
         "openstack.services.block-store",
         "openstack.services.volume",
         "openstack.services.volumev3"),
    )
    policy_rules = (("volume", "context_is_admin"),)
