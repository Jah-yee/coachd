---
title: Install
weight: 2
---

From an empty Docker host to a working coachd: Garmin data in, an agent connected, and your first passkey enrolled.

## The stack

`deploy/compose.yaml` runs coachd with Tailscale (the private lane), Grafana Alloy, VictoriaMetrics and VictoriaLogs. Two optional overrides add Grafana Cloud (`compose.cloud.yaml`) and a self-hosted Grafana (`compose.local.yaml`); see [monitoring](/docs/operations/monitoring/).

The public lane is a `cloudflared` **on the host** (systemd, dashboard-managed tunnel) or a reverse proxy such as Caddy, forwarding `coachd.<your-domain>` to `http://127.0.0.1:8080`. coachd publishes only that listener, on loopback. Ports 8081 (private) and 9090 (ops, unauthenticated pprof) are never published, and there is no cloudflared container.

Layout on the host (for example `~/coachd/deploy`): `compose.yaml`, `compose.cloud.yaml`, `compose.local.yaml`, `.env`, `alloy/`, `grafana/`, `restic/`, `tailscale/`, `secrets/`. Run every command in these pages from this directory.

## Steps

{{% steps %}}

### Get the stack files

Clone the repository on the host and copy the settings template:

```bash
git clone https://github.com/stefiix92/coachd.git
cd coachd/deploy
cp .env.example .env
```

`.env` holds non-secret settings only; tokens live in `secrets/`. Set `COACHD_TIMEZONE` (for example `Europe/Bratislava`). The [configuration reference](/docs/reference/configuration/) lists every setting.

### Put the data on an encrypted disk

`/data` (the `coachd-data` volume) holds health data, the Garmin raw JSON and FIT files, and token hashes. It **must be on an encrypted disk**. A plain SD card is not encrypted: put Docker's data root (or just this volume) on a LUKS-encrypted USB drive or SSD, or use LUKS on the root. Unlocking at boot is up to you; coachd does not manage disk encryption.

### Get the image

Run the signed release image, or build your own: [Get the image](image/).

### Create the secrets

The Garmin key, the Tailscale auth key and the ntfy token are files in `secrets/`, each owned by the container that reads it: [Secrets](secrets/).

### Add your Garmin key

[Garmin key](garmin-key/): create it, save it and set its expiry date.

### Open the public lane

Pick one mode:

- [Cloudflare Access](cloudflare-access/): Cloudflare does the login and MFA, and its Managed OAuth signs in the AI apps.
- [Built-in auth](builtin-auth/): coachd does it itself, with your passkey for the page and its own OAuth 2.1 server for the AI apps. No Cloudflare account needed.

### Open the private lane

[Tailscale](tailscale/), for agents on your own devices.

### Enrol your passkey

[Enrol a passkey](passkey/) once the public lane works. You need it to approve workouts.

### Connect an agent

[Connect Claude, ChatGPT or a local client](/docs/agents/).

{{% /steps %}}

## Optional extras

{{< cards >}}
  {{< card link="notifications" title="ntfy and the post-run webhook" icon="bell" subtitle="A phone notice when a workout is ready; an event for your automation when a run arrives." >}}
  {{< card link="../watch" title="Watch app" icon="clock" subtitle="Approved workouts copied onto your watch." >}}
  {{< card link="../operations/monitoring" title="Monitoring" icon="chart-square-bar" subtitle="A local Grafana with dashboards and alerts, or Grafana Cloud." >}}
  {{< card link="../operations/backups" title="Off-site backups" icon="cloud-upload" subtitle="restic to S3, B2 or SFTP." >}}
{{< /cards >}}
