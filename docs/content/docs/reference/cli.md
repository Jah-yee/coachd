---
title: Command line
weight: 2
---

Run them from `deploy/` with `docker compose exec coachd /coachd <command>`.

## serve

Runs coachd; the container's default command.

## token

Private-lane agent tokens.

```bash
docker compose exec coachd /coachd token create --name claude-code --scope read --ttl 90d
docker compose exec coachd /coachd token list
docker compose exec coachd /coachd token revoke <id>
```

- `--name`: required, 1 to 64 characters.
- `--scope`: `read` or `write` (write includes read). See [read or write](/docs/agents/#read-or-write).
- `--ttl`: lifetime, for example `90d` or `24h`; default `90d`, at most `365d`.

`create` prints the token once on stdout; store it now. `list` shows each token's id, name, scopes, creation, expiry, last use and revocation.

## device

Watch app pairings.

```bash
docker compose exec coachd /coachd device pair <code> --label fenix
docker compose exec coachd /coachd device list
docker compose exec coachd /coachd device revoke <id>
```

`pair` confirms the 6-digit code the watch shows (codes expire after 10 minutes). The label defaults to "watch &lt;date&gt;" (in `COACHD_TIMEZONE`, else UTC), one line of at most 40 characters. See [pairing](/docs/watch/pairing/).

## enroll

```bash
docker compose exec coachd /coachd enroll
```

Prints a passkey enrolment link, valid for 10 minutes. See [Enrol a passkey](/docs/install/passkey/).

## backup

```bash
docker compose exec coachd /coachd backup
```

Writes a database snapshot and its manifest to `/data/backups`. See [backups](/docs/operations/backups/).

## restore

```bash
docker compose stop coachd
docker compose run --rm --no-deps coachd restore /data/backups/coachd-<ts>.db
docker compose start coachd
```

Swaps in a snapshot; refuses while `serve` runs. See [restore](/docs/operations/backups/#restore).

## healthcheck

Probes the ops listener's `/livez`; the compose healthcheck uses it. It needs no configuration beyond `COACHD_OPS_ADDR`.

## version

Prints the version.
