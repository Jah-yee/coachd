---
title: Monitoring
weight: 2
---

coachd sends OpenTelemetry to Grafana Alloy, the only endpoint it knows. Alloy routes each signal:

| Signal | Stored |
|---|---|
| Metrics | VictoriaMetrics, on the host |
| Logs | VictoriaLogs, on the host (30 days, capped at 5 GiB) |
| Traces | [Grafana Cloud](../grafana-cloud/) Tempo (optional) |
| Profiles | [Grafana Cloud](../grafana-cloud/) Pyroscope (optional) |

To look at them, run a **local Grafana** on the host, reached over Tailscale (below), or let [Grafana Cloud](../grafana-cloud/) read the host stores through Private Data Source Connect. The dashboard and the alert rules are kept in the repo, in [`deploy/grafana/`](https://github.com/stefiix92/coachd/tree/main/deploy/grafana).

Logs, metrics, traces and profiles carry IDs, timings and outcomes only, enforced by an allowlist with tests: never health data, chat content or tokens.

## Local Grafana

`compose.local.yaml` adds a self-hosted Grafana (OSS 12.4) with the repo's dashboard (`grafana/dashboards/coachd.json`), the six alert rules (`grafana/alerts/coachd-alerts.yaml`) and both data sources (VictoriaMetrics `coachd-vm`, VictoriaLogs `coachd-vl`) provisioned from files, read-only in the UI.

No host port is published: only the Tailscale node reaches it, at `https://raspberrypi-coachd.<tailnet>.ts.net/grafana/`, on the same `tag:coachd` grant on `tcp:443` as the MCP lane, so the ACL does not change. The override swaps the tailscale serve config for `tailscale/serve-local.json`: `serve.json` plus a `/grafana/` handler, still no Funnel.

### Secrets

Both owned `472:472`, mode `0400`:

- `grafana_admin_password`: the `admin` password. Create it without echoing it.
- `grafana_alert_url`: the full ntfy topic URL for alert notifications. It is a secret (the topic is a bearer secret). The file must exist; empty means no notifications (alerts still show in Grafana).

For the alert URL, append `?template=grafana` so ntfy renders Grafana's webhook JSON as a readable message, and check that your ntfy server supports templates by sending a test from Grafana (Alerting → Contact points → ntfy → Test). For a protected topic add ntfy's `auth` query parameter: raw base64 of the `Authorization` header value, for example `read -rs NTFY_TOKEN; printf 'Bearer %s' "$NTFY_TOKEN" | base64 -w0 | tr -d '='` (ntfy docs, "Query param").

{{< callout type="warning" >}}
The file must hold one `https://` URL on one line, without quotes, backslashes, pipes, spaces or `$`. Grafana expands `$VAR` in provisioned values, so a `$` could pull its environment, the admin password included, into the URL. Anything else stops Grafana at start with an error.
{{< /callout >}}

```bash
umask 077
openssl rand -base64 24 > secrets/grafana_admin_password
: > secrets/grafana_alert_url        # empty: notifications off; or, with the URL:
# read -rs ALERT_URL && printf '%s' "$ALERT_URL" > secrets/grafana_alert_url
docker run --rm --network none -v "$PWD/secrets:/s" --entrypoint sh \
  tailscale/tailscale:v1.104.2@sha256:b11583ad2e93e4671128538c88cf11f460444a5d7e449311dd1933a400ab849d \
  -c 'cd /s && chown 472:472 grafana_admin_password grafana_alert_url && chmod 0400 grafana_admin_password grafana_alert_url'
```

Type `https://<ntfy host>/<secret topic>?template=grafana` at the hidden `read -rs` prompt, so it stays out of shell history (or leave the file empty). The URL reaches Grafana only through the secret file: the entrypoint (`grafana/local/entrypoint.sh`) writes it into the contact point in the container's tmpfs using shell builtins only, so it is in no process's argv, the compose files, the environment or `docker inspect`. Grafana stores it in its database (`grafana-data` volume) and writes it as `<redacted>` in delivery errors, though a DNS failure still names the ntfy host (not the topic).

### Turn it on

{{% steps %}}

### Set Grafana's URL

```bash {filename=".env"}
GRAFANA_ROOT_URL=https://raspberrypi-coachd.<tailnet>.ts.net/grafana/
```

### Enable the override

Alone or with Grafana Cloud, in `.env`:

```bash {filename=".env"}
COMPOSE_FILE=compose.yaml:compose.local.yaml
# or: COMPOSE_FILE=compose.yaml:compose.cloud.yaml:compose.local.yaml
```

then `docker compose up -d` (this also recreates tailscale with `serve-local.json`).

### Sign in

Open `https://raspberrypi-coachd.<tailnet>.ts.net/grafana/` and sign in as `admin`. To read the password once (into your password manager):

```bash
docker compose exec grafana cat /run/secrets/grafana_admin_password
```

{{% /steps %}}

### Changing it later

- Grafana sets the admin password from the file only when it first creates its database; later changes to the file have no effect. Change it in the UI (profile → Change password), or reset the database as below.
- After changing `grafana_alert_url` (rewrite it as in [Secrets](/docs/install/secrets/#changing-a-secret)), run `docker compose up -d --force-recreate grafana`: the entrypoint reads it only at start. A new URL replaces the old one in the ntfy contact point. To turn notifications off, empty the file and run the same command.
- The notification policy tree is file-managed: each start with a URL sets it to route coachd alerts to ntfy, and **each start with an empty URL resets it to Grafana's default** and deletes the ntfy contact point, so policy changes made in the UI do not survive a restart.

If Grafana's database ever gets out of step with the files, reset it. Everything in it is provisioned from files again, the admin password is re-read from its file and the plugin is downloaded again:

```bash
docker compose rm -sf grafana
docker volume rm coachd_grafana-data
docker compose up -d grafana
```

To disable local monitoring, drop `compose.local.yaml` from `COMPOSE_FILE` and run `docker compose up -d --remove-orphans` (tailscale goes back to `serve.json`; the `grafana-data` volume stays until you remove it).

### Hardening and limits

- Grafana runs hardened like the rest (uid 472, read-only rootfs, no capabilities, `no-new-privileges`, a 16 MB tmpfs at `/tmp` for the assembled provisioning) and adds 256 MB to the memory budget.
- Networks: `grafana-data` (internal, shared only with VictoriaMetrics and VictoriaLogs), `grafana-ui` (internal, shared only with tailscale) and its own `grafana-egress` (plugin download, ntfy). Grafana is not on `internal`, so it cannot reach coachd or Alloy; tailscale never joins `grafana-data`.
- Traces and profiles stay Grafana Cloud only; locally the dashboard's traces panel is empty (there is no Tempo data source).
- The first start downloads the VictoriaLogs plugin (pinned version, about 80 MB; about 215 MB unpacked in `grafana-data`, as the package carries every platform's binary) from grafana.com, so the first healthy start takes longer.
- Plugin signatures are checked with the key built into Grafana (no periodic key download), and plugin installs from the UI catalog are off (`plugin_admin_enabled=false`; Grafana's `/api/gnet` proxy to grafana.com still answers a signed-in user's request). Update checks, usage reporting, the news feed, Gravatar and external snapshots are off, and the four Drilldown apps Grafana 12 would preinstall are disabled. Nothing else leaves the host on its own, apart from alert notifications to your ntfy server.
- Remaining exposure: through `grafana-egress`, Grafana (and any data source an admin creates) can still reach the internet, the LAN and the host's addresses, as any container with outbound access can.

## Alerts

Six Grafana-managed rules, in `deploy/grafana/alerts/coachd-alerts.yaml`. To check one, run its query in Explore against the Prometheus data source.

| Alert | PromQL | Fires when |
|---|---|---|
| `GarminSyncFailing` (critical, 10 m) | `max(coachd_garmin_sync_last_success_age_seconds)` | > 10800: any one data type without a successful sync for over 3 h |
| `GarminKeyExpiring` (warning) | `coachd_garmin_key_days_left_days or coachd_garmin_key_days_left` | <= 7 |
| `AuthFailureSpike` (warning) | `sum(increase(coachd_auth_failures_total{lane!="device"}[10m]))` | > 20 on the public or private lane (the open device path is excluded: internet scanners hit it) |
| `WorkoutDeliveryFailed` (warning) | `increase(coachd_proposals_deliveries_total{outcome="failed"}[1h])` | > 0: a fetched workout was not acknowledged within 15 min (coachd starts the series at 0, so the first failure counts) |
| `BackupStale` (warning, 10 m) | `max(coachd_backup_age_seconds)` | > 172800, or no data: no backup in over 48 h |
| `DataDiskFull` (critical, 15 m) | `coachd_data_disk_used_ratio` | > 0.85 |

No-data handling is per rule:

- `GarminSyncFailing`, `DataDiskFull` and `BackupStale` use `noDataState: Alerting`, so a dead coachd or Alloy (series disappear) still pages, and so does a missing backup age (no backup yet, or the newest named more than 1 h in the future).
- `GarminKeyExpiring` uses `NoData` (visible, not paged as a failure).
- `AuthFailureSpike` uses `OK` (no series means no failures).

Key days left is declared with unit `d`; Alloy's OTel→Prometheus conversion appends the unit, so the series is most likely `coachd_garmin_key_days_left_days`. The rule and panel accept either name.

## Dashboard notes

- The logs panel uses LogsQL `{"service.name"="coachd"} severity_text:in("WARN","ERROR")`. VictoriaLogs stores the OTLP severity as `severity_text` (`INFO`, `WARN`, `ERROR`) and `service.name` as a stream field.
- The traces panel needs Tempo in Grafana Cloud with `compose.cloud.yaml` enabled (traces are dropped otherwise).
- The Go runtime row uses the `go_*` series from `otel/contrib/instrumentation/runtime` (`go_goroutine_count`, `go_memory_used_bytes`, `go_memory_allocated_bytes_total`); HTTP panels use otelhttp's `http_server_request_duration_seconds` with `server_address` = `coachd-public` / `coachd-private`.
