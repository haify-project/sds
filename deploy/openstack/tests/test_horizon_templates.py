"""The plugin's templates compile. Needs Django; skipped without it."""

import os
import unittest

TEMPLATES = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "haify_horizon", "templates")

try:
    import django
    from django.conf import settings
except ImportError:  # pragma: no cover
    django = None
else:
    if not settings.configured:
        settings.configure(USE_I18N=True)
        django.setup()
    from django.template import Engine


@unittest.skipIf(django is None, "Django is not installed")
class Templates(unittest.TestCase):
    def test_compile(self):
        # Horizon's own tags are not needed by these templates; url, i18n,
        # if/for and blocktrans are Django's.
        engine = Engine(dirs=[TEMPLATES], libraries={"i18n": "django.templatetags.i18n"})
        for name in ("haify/_volume_tab.html",):
            engine.get_template(name)
        # index.html extends Horizon's base.html, so only its own tags are checked.
        with open(os.path.join(TEMPLATES, "haify", "index.html")) as f:
            source = f.read().replace("{% extends 'base.html' %}", "")
        engine.from_string(source)


if __name__ == "__main__":
    unittest.main()
