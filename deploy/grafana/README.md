# coachd Grafana dashboard and alerts

- `dashboards/coachd.json`: the dashboard (Grafana 12 compatible, schema 39).
- `alerts/coachd-alerts.yaml`: six Grafana-managed alert rules (data source UID `coachd-vm`).
- `alerts/notifications.example.yaml`: an ntfy contact point and a notification policy, as examples.
- `local/`: provisioning for the self-hosted Grafana of `../compose.local.yaml`: data sources (`coachd-vm`, `coachd-vl`), the dashboards provider, `notifications.yaml.tmpl` and `entrypoint.sh`, which assembles them at start.

The local Grafana provisions all of this automatically: see [Monitoring](https://stefiix92.github.io/coachd/docs/operations/monitoring/), which also lists the alerts and how to check them. Importing the dashboard and the rules into Grafana Cloud is in [Grafana Cloud](https://stefiix92.github.io/coachd/docs/operations/grafana-cloud/#dashboard-and-alerts).
