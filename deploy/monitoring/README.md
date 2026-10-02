# Monitoring

Prometheus and Grafana for SDS controllers (`[metrics] enabled = true`).

```bash
# one target per cluster: its controller VIP and metrics port
vi prometheus/prometheus.yml
printf 'GF_SECURITY_ADMIN_USER=admin\nGF_SECURITY_ADMIN_PASSWORD=%s\n' "$(openssl rand -hex 16)" > .env
chmod 600 .env
docker compose up -d
```

| | Port |
| --- | --- |
| Prometheus | 39417 |
| Grafana (dashboard "SDS") | 41863 |

`prometheus-rules.yml` holds the alerting rules; Prometheus loads it as
`/etc/sds/prometheus-rules.yml`. Route them with an Alertmanager if the
controller's own notification channels are not enough.
