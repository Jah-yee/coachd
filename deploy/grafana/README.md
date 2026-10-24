# coachd Grafana dashboard and alerts

Files:

- `dashboards/coachd.json` - dashboard (Grafana 12 compatible, schema 39).
- `alerts/coachd-alerts.yaml` - six Grafana-managed alert rules (data source UID `coachd-vm`).
- `alerts/notifications.example.yaml` - an ntfy contact point and a notification policy, as examples.
- `local/` - provisioning for the self-hosted Grafana of `../compose.local.yaml`: data sources (`coachd-vm`,
  `coachd-vl`), the dashboards provider, `notifications.yaml.tmpl` (the example above with the URL taken from
  `secrets/grafana_alert_url`) and `entrypoint.sh`, which assembles them at start.

The local Grafana (`../README.md` section 6) provisions the dashboard, the rules and the data sources
automatically, so the rest of this file is for Grafana Cloud only.

For Grafana Cloud, data sources must already exist (see `../README.md` section 5): Prometheus -> `http://victoriametrics:8428`,
VictoriaLogs plugin -> `http://victorialogs:9428`, and Tempo. All three are reached from Grafana Cloud over PDC.
Metrics carry no health values; keep it that way.

## Placeholders to replace

| Placeholder | Where | Value |
|---|---|---|
| `coachd-vm` | `alerts/coachd-alerts.yaml` (every rule) | Not a placeholder for the local Grafana, which provisions its VictoriaMetrics data source with this fixed UID. On Grafana Cloud, create the data source with this UID or rewrite it (the UI cannot set a UID): see below |
| `REPLACE_WITH_NTFY_HOST` / `REPLACE_WITH_NTFY_TOPIC` | `alerts/notifications.example.yaml`, contact point `url` | your ntfy host and topic. The topic is a secret: do not commit it |
| `GRAFANA_URL`, `GRAFANA_TOKEN` | shell env | e.g. `https://<stack>.grafana.net`; a service-account token with Editor role (alerting + dashboards write) |

The dashboard has no hard-coded UIDs. It asks for three data source variables (`ds_prometheus`, `ds_vlogs`, `ds_tempo`) and you pick them at the top of the dashboard.

### Data source UID

Either create the Prometheus data source through the API with the UID the rules expect (token from env, never inline):

```bash
curl -fsS -X POST "$GRAFANA_URL/api/datasources" \
  -H "Authorization: Bearer $GRAFANA_TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"VictoriaMetrics","type":"prometheus","uid":"coachd-vm","url":"http://victoriametrics:8428","access":"proxy"}'
```

then enable Private Data Source Connect on it in the UI. Or, if you already have the data source under another UID, rewrite a copy before applying:

```bash
sed 's/coachd-vm/<your uid>/g' deploy/grafana/alerts/coachd-alerts.yaml > /tmp/coachd-alerts.yaml
```

and use that copy in step 2 below.

## Import the dashboard

UI: Dashboards > New > Import > upload `dashboards/coachd.json`.

API (token from env, never inline):

```bash
export GRAFANA_URL=https://<stack>.grafana.net
export GRAFANA_TOKEN=...   # from a secret store, not shell history
jq '{dashboard: ., overwrite: true}' deploy/grafana/dashboards/coachd.json |
  curl -fsS -X POST "$GRAFANA_URL/api/dashboards/db" \
    -H "Authorization: Bearer $GRAFANA_TOKEN" -H 'Content-Type: application/json' -d @-
```

## Apply the alert rules (provisioning API)

Grafana Cloud has no file provisioning, so the YAML is applied through the alerting provisioning API.
Requires `jq` and `yq` (mikefarah). Run from the repo root after replacing the placeholders.

```bash
H=(-H "Authorization: Bearer $GRAFANA_TOKEN" -H 'Content-Type: application/json')

# 1. folder
FOLDER_UID=$(curl -fsS -X POST "$GRAFANA_URL/api/folders" "${H[@]}" -d '{"title":"coachd"}' | jq -r .uid)
# (if the folder exists: FOLDER_UID=$(curl -fsS "$GRAFANA_URL/api/folders" "${H[@]}" | jq -r '.[]|select(.title=="coachd").uid'))

# 2. rules, one POST each
yq -o=json '.groups[0]' deploy/grafana/alerts/coachd-alerts.yaml |
  jq -c --arg f "$FOLDER_UID" '.name as $g | .interval as $i | .rules[] | del(.uid) | . + {folderUID:$f, ruleGroup:$g, orgID:1}' |
  while read -r rule; do
    curl -fsS -X POST "$GRAFANA_URL/api/v1/provisioning/alert-rules" "${H[@]}" \
      -H 'X-Disable-Provenance: true' -d "$rule" | jq -r .title
  done

# 3. contact point
yq -o=json '.contactPoints[0].receivers[0] | del(.uid) | . + {name:"ntfy"}' deploy/grafana/alerts/notifications.example.yaml |
  curl -fsS -X POST "$GRAFANA_URL/api/v1/provisioning/contact-points" "${H[@]}" -H 'X-Disable-Provenance: true' -d @-

# 4. notification policy (replaces the whole policy tree: check yours first)
yq -o=json '.policies[0] | del(.orgId)' deploy/grafana/alerts/notifications.example.yaml |
  curl -fsS -X PUT "$GRAFANA_URL/api/v1/provisioning/policies" "${H[@]}" -H 'X-Disable-Provenance: true' -d @-
```

The new group gets the default 60 s evaluation interval. Step 4 overwrites the root policy; if you already
route other alerts, add the `service = coachd` route in the UI (Alerting > Notification policies) instead.
Rules are idempotent only by title/group: delete a rule in the UI before re-POSTing, or use PUT per rule UID.

The local Grafana provisions `alerts/coachd-alerts.yaml` as is, and the ntfy contact point from `local/notifications.yaml.tmpl` when `secrets/grafana_alert_url` is set; see `../README.md` section 6.

## Alert sanity checks

Run these in Explore against the Prometheus data source; each should return the value the rule evaluates.

| Alert | PromQL | Fires when |
|---|---|---|
| `GarminSyncFailing` (critical, 10 m) | `max(coachd_garmin_sync_last_success_age_seconds)` | > 10800: any one data type without a successful sync for over 3 h |
| `GarminKeyExpiring` (warning) | `coachd_garmin_key_days_left_days or coachd_garmin_key_days_left` | <= 7 |
| `AuthFailureSpike` (warning) | `sum(increase(coachd_auth_failures_total{lane!="device"}[10m]))` | > 20 on the public or private lane (the open device path is excluded: internet scanners hit it) |
| `WorkoutDeliveryFailed` (warning) | `increase(coachd_proposals_deliveries_total{outcome="failed"}[1h])` | > 0: a fetched workout was not acknowledged within 15 min (coachd starts the series at 0, so the first failure counts) |
| `BackupStale` (warning, 10 m) | `max(coachd_backup_age_seconds)` | > 172800, or no data: no backup in over 48 h |
| `DataDiskFull` (critical, 15 m) | `coachd_data_disk_used_ratio` | > 0.85 |

Key days left is declared with unit `d`; Alloy's OTel->Prometheus conversion appends the unit, so the series is
most likely `coachd_garmin_key_days_left_days`. The rule and panel accept either name. No-data handling is per rule: GarminSyncFailing, DataDiskFull and BackupStale use `noDataState: Alerting`, so a dead coachd or Alloy (series disappear) still pages, and so does a missing backup age (no backup yet, or the newest named more than 1 h in the future); GarminKeyExpiring uses `NoData` (visible, not paged as a failure); AuthFailureSpike uses `OK` (no series means no failures).

## Notes

- The logs panel uses LogsQL `{"service.name"="coachd"} severity_text:in("WARN","ERROR")`. VictoriaLogs stores the
  OTLP severity as `severity_text` (`INFO`, `WARN`, `ERROR`) and `service.name` as a stream field; both verified
  against live data.
- The traces panel needs Tempo in Grafana Cloud with `deploy/compose.cloud.yaml` enabled (traces are dropped otherwise);
  in the local Grafana it stays empty.
- Go runtime row uses the `go_*` series from `otel/contrib/instrumentation/runtime` (`go_goroutine_count`,
  `go_memory_used_bytes`, `go_memory_allocated_bytes_total`); HTTP panels use otelhttp's
  `http_server_request_duration_seconds` with `server_address` = `coachd-public` / `coachd-private`.
