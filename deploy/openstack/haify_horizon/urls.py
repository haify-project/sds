from django.urls import re_path

from haify_horizon import views

urlpatterns = [
    re_path(r"^$", views.IndexView.as_view(), name="index"),
]
