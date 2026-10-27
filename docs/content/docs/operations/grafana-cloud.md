---
title: Grafana Cloud
weight: 3
---

Off by default. With it on, traces go to Grafana Cloud Tempo, profiles to Pyroscope, and Grafana Cloud reads the host's VictoriaMetrics and VictoriaLogs through Private Data Source Connect (PDC).

## Turn it on

{{% steps %}}

### Store the tokens

```bash
put_secret grafana_cloud_otlp_token 473:473        # the helper from Secrets
put_secret grafana_cloud_pyroscope_token 473:473
put_secret grafana_cloud_pdc_token 30000:30000
```

### Fill the settings

The `GRAFANA_CLOUD_*` and `GRAFANA_PDC_*` variables in `.env`: see the [configuration reference](/docs/reference/configuration/#grafana).

### Add the override

```bash {filename=".env"}
COMPOSE_FILE=compose.yaml:compose.cloud.yaml
```

Then `docker compose up -d`. With `COMPOSE_FILE` in `.env`, a bare `docker compose up -d` keeps it on.

{{% /steps %}}

To turn it off, remove that line and run `docker compose up -d --remove-orphans`.

## Data sources

In Grafana Cloud, add the data sources through the PDC network:

- Prometheus → `http://victoriametrics:8428`
- VictoriaLogs plugin → `http://victorialogs:9428`
- Tempo, with the trace-to-logs custom query `trace_id:="${__trace.traceId}"`


## Dashboard and alerts

Files in `deploy/grafana/`:

- `dashboards/coachd.json`: the dashboard (Grafana 12 compatible, schema 39). It has no hard-coded UIDs: it asks for three data source variables (`ds_prometheus`, `ds_vlogs`, `ds_tempo`) that you pick at the top of the dashboard.
- `alerts/coachd-alerts.yaml`: the six [alert rules](../monitoring/#alerts), for data source UID `coachd-vm`.
- `alerts/notifications.example.yaml`: an ntfy contact point and a notification policy, as examples.

| Placeholder | Where | Value |
|---|---|---|
| `coachd-vm` | `alerts/coachd-alerts.yaml` (every rule) | Create the data source with this UID or rewrite it (the UI cannot set a UID): see below. |
| `REPLACE_WITH_NTFY_HOST` / `REPLACE_WITH_NTFY_TOPIC` | `alerts/notifications.example.yaml`, contact point `url` | Your ntfy host and topic. The topic is a secret: do not commit it. |
| `GRAFANA_URL`, `GRAFANA_TOKEN` | shell environment | For example `https://<stack>.grafana.net`; a service-account token with the Editor role (alerting and dashboards write). |

### Data source UID

Either create the Prometheus data source through the API with the UID the rules expect (token from the environment, never inline):

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

### Import the dashboard

In the UI: Dashboards → New → Import → upload `dashboards/coachd.json`. Or through the API:

```bash
export GRAFANA_URL=https://<stack>.grafana.net
export GRAFANA_TOKEN=...   # from a secret store, not shell history
jq '{dashboard: ., overwrite: true}' deploy/grafana/dashboards/coachd.json |
  curl -fsS -X POST "$GRAFANA_URL/api/dashboards/db" \
    -H "Authorization: Bearer $GRAFANA_TOKEN" -H 'Content-Type: application/json' -d @-
```

### Apply the alert rules

Grafana Cloud has no file provisioning, so the YAML goes through the alerting provisioning API. This needs `jq` and `yq` (mikefarah). Run from the repo root after replacing the placeholders:

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

The new group gets the default 60 s evaluation interval. Step 4 overwrites the root policy; if you already route other alerts, add the `service = coachd` route in the UI (Alerting → Notification policies) instead. Rules are idempotent only by title and group: delete a rule in the UI before re-POSTing, or use PUT per rule UID.

## The PDC agent

`pdc-agent` takes its token only as a command-line flag. It is read from the secret file at start, so it isn't in the compose files or `docker inspect`, but it is visible in the process list to every user on the host: treat the host as trusted.

The agent shares an internal network only with VictoriaMetrics and VictoriaLogs, so neither it nor a Grafana Cloud data source can reach coachd, Alloy or Tailscale.
