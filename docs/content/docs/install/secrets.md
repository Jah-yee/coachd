---
title: Secrets
weight: 2
---

Secrets are files in `deploy/secrets/` (git-ignored), mounted into the containers at `/run/secrets/<name>`. Never put tokens in `.env`.

## The files

| File | Owner:group | Mode | Read by | Needed |
|---|---|---|---|---|
| `garmin_api_key` | `65532:65532` | `0400` | coachd (uid 65532) | always: [Garmin key](../garmin-key/) |
| `ntfy_token` | `65532:65532` | `0400` | coachd | always; may be empty: [ntfy](../notifications/) |
| `tailscale_authkey` | `0:0` | `0400` | tailscale (container root, as file owner, no capabilities) | always: [Tailscale](../tailscale/) |
| `grafana_cloud_otlp_token` | `473:473` | `0400` | alloy (uid 473) | with Grafana Cloud |
| `grafana_cloud_pyroscope_token` | `473:473` | `0400` | alloy | with Grafana Cloud |
| `grafana_cloud_pdc_token` | `30000:30000` | `0400` | pdc-agent (uid 30000) | with Grafana Cloud |
| `grafana_admin_password` | `472:472` | `0400` | local grafana (uid 472) | with local monitoring |
| `grafana_alert_url` | `472:472` | `0400` | local grafana | with local monitoring; may be empty |

Compose mounts every file its services use, so `docker compose up` fails ("bind source path does not exist") until each one exists, even one whose feature you don't use. [Grafana Cloud](/docs/operations/grafana-cloud/) and [local monitoring](/docs/operations/monitoring/#local-grafana) explain their own files; the [restic password](/docs/operations/backups/#off-site-with-restic) and the optional [webhook token](../notifications/#post-run-webhook) follow the same pattern.

## Writing a secret

coachd refuses a key file whose mode grants any group or other permission, and each container runs as its own user. So write the file as your normal user, then hand it to its owner with a throwaway container. This works from the docker group, with no sudo; the container has no network and nothing else mounted.

Run from `deploy/`. `<owner>` is `65532:65532`, `0:0`, `473:473`, `30000:30000` or `472:472`, as in the table:

```bash
umask 077
read -rs KEY                                           # hidden prompt: stays out of shell history
printf '%s' "$KEY" > secrets/garmin_api_key            # printf: no trailing newline
docker run --rm --network none -v "$PWD/secrets:/s" --entrypoint sh \
  tailscale/tailscale:v1.104.2@sha256:b11583ad2e93e4671128538c88cf11f460444a5d7e449311dd1933a400ab849d \
  -c 'chown 65532:65532 /s/garmin_api_key && chmod 0400 /s/garmin_api_key'
```

{{< callout type="warning" >}}
Token files must not end in a newline: use `printf '%s'`, never `echo`.
{{< /callout >}}

## Changing a secret

After the chown you can no longer edit the file as your user. The directory stays yours, so `rm` the file, recreate it the same way and chown it again (or rewrite it with `sudo`).
