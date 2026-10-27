# coachd deployment

The install and operations guide lives on the documentation site: **[stefiix92.github.io/coachd/docs/install](https://stefiix92.github.io/coachd/docs/install/)**. Its source is in [`docs/content/docs/`](../docs/content/docs/).

This directory holds the stack: `compose.yaml` (coachd, Tailscale, Grafana Alloy, VictoriaMetrics, VictoriaLogs), the optional `compose.cloud.yaml` (Grafana Cloud) and `compose.local.yaml` (self-hosted Grafana), `.env.example`, and the `alloy/`, `grafana/`, `restic/`, `tailscale/` and `secrets/` directories. Run every command from here.

Where the former sections of this file went:

| Section | Page |
|---|---|
| Build or load the image | [Get the image](https://stefiix92.github.io/coachd/docs/install/image/) |
| 1. Secrets | [Secrets](https://stefiix92.github.io/coachd/docs/install/secrets/) |
| 2. Garmin key | [Garmin key](https://stefiix92.github.io/coachd/docs/install/garmin-key/) |
| 3. Cloudflare (public lane) | [Cloudflare Access](https://stefiix92.github.io/coachd/docs/install/cloudflare-access/), [Enrol a passkey](https://stefiix92.github.io/coachd/docs/install/passkey/) |
| 4. Tailscale (private lane) | [Tailscale](https://stefiix92.github.io/coachd/docs/install/tailscale/) |
| 5. Grafana Cloud | [Grafana Cloud](https://stefiix92.github.io/coachd/docs/operations/grafana-cloud/) |
| 6. Local monitoring | [Monitoring](https://stefiix92.github.io/coachd/docs/operations/monitoring/) |
| 7. Encrypted `/data` | [Install](https://stefiix92.github.io/coachd/docs/install/#encrypt-the-data-disk) |
| 8. ntfy, post-run webhook | [ntfy and webhook](https://stefiix92.github.io/coachd/docs/install/notifications/) |
| 9. Built-in auth | [Built-in auth](https://stefiix92.github.io/coachd/docs/install/builtin-auth/) |
| 10. Watch app | [Watch app](https://stefiix92.github.io/coachd/docs/watch/) |
| 11. Backups and restore | [Backups and restore](https://stefiix92.github.io/coachd/docs/operations/backups/) |
| Notes | [Security model](https://stefiix92.github.io/coachd/docs/reference/security/), [Upgrading](https://stefiix92.github.io/coachd/docs/operations/upgrading/), [Configuration](https://stefiix92.github.io/coachd/docs/reference/configuration/) |
