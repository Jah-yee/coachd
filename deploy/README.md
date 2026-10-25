# coachd deployment

Stack: coachd + Tailscale (private lane) + Grafana Alloy + VictoriaMetrics + VictoriaLogs.
Optional Grafana Cloud export and PDC agent via `compose.cloud.yaml`; optional self-hosted
Grafana over Tailscale via `compose.local.yaml` (section 6).

Public lane: a **host** `cloudflared` (systemd, dashboard-managed tunnel) forwards
`coachd.<your-domain>` to `http://127.0.0.1:8080`. coachd publishes only its public
listener, on `127.0.0.1:8080`. Ports 8081 (private) and 9090 (ops, unauthenticated pprof)
are never published. There is no cloudflared container. Section 3 puts Cloudflare Access in
front of the page and `/mcp`; section 9 is the alternative without Access, coachd's built-in auth.

Layout on the host (e.g. `~/coachd/deploy`): `compose.yaml`, `compose.cloud.yaml`,
`compose.local.yaml`, `.env`, `alloy/`, `grafana/`, `restic/`, `tailscale/`, `secrets/`. Run all commands from this directory.

## Build or load the image

**Release image (recommended once releases exist).** Each tagged release publishes a multi-arch
image to `ghcr.io/stefiix92/coachd`. The image is signed with Sigstore keyless signing by the
release workflow, and carries an SBOM and build provenance. Take the digest from the release notes
and verify it before you run it ([cosign](https://docs.sigstore.dev/cosign/system_config/installation/)):

```bash
cosign verify ghcr.io/stefiix92/coachd@sha256:<digest> \
  --certificate-identity-regexp '^https://github.com/stefiix92/coachd/.github/workflows/release.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Then set `COACHD_IMAGE=ghcr.io/stefiix92/coachd:<version>@sha256:<digest>` in `.env` and run
`docker compose pull coachd && docker compose up -d --no-build coachd`. Pinning the digest means
a re-pushed tag can never change what you run.

**Build it yourself.** On the host, from a checkout: `docker compose build` (builds `coachd:local`).
Or build elsewhere and load: `docker buildx build --platform linux/arm64 --load -t coachd:local ..`
then `docker save coachd:local | ssh pi docker load`.

## 1. Secrets

Secrets are files in `secrets/` (git-ignored), mounted at `/run/secrets/<name>`. coachd
refuses a key file whose mode grants any group/other permission.

| File | Owner:group | Mode | Read by |
|---|---|---|---|
| `garmin_api_key` | `65532:65532` | `0400` | coachd (uid 65532) |
| `tailscale_authkey` | `0:0` | `0400` | tailscale (container root, as file owner, no capabilities) |
| `ntfy_token` | `65532:65532` | `0400` | coachd; must exist, may be empty (see section 8) |
| `grafana_cloud_otlp_token` | `473:473` | `0400` | alloy (uid 473) |
| `grafana_cloud_pyroscope_token` | `473:473` | `0400` | alloy |
| `grafana_cloud_pdc_token` | `30000:30000` | `0400` | pdc-agent (uid 30000) |
| `grafana_admin_password` | `472:472` | `0400` | local grafana (uid 472), section 6 |
| `grafana_alert_url` | `472:472` | `0400` | local grafana; must exist, may be empty (section 6) |

Write the file as your normal user, then hand it to the owner with a throwaway container
(works from the docker group, no sudo; `--network none`, nothing else mounted). Run from this
directory; `<owner>` is `65532:65532`, `0:0`, `473:473`, `30000:30000` or `472:472` per the table:

```bash
umask 077
read -rs KEY                                           # hidden prompt: stays out of shell history
printf '%s' "$KEY" > secrets/garmin_api_key            # printf: no trailing newline
docker run --rm --network none -v "$PWD/secrets:/s" --entrypoint sh \
  tailscale/tailscale:v1.104.2@sha256:b11583ad2e93e4671128538c88cf11f460444a5d7e449311dd1933a400ab849d \
  -c 'chown 65532:65532 /s/garmin_api_key && chmod 0400 /s/garmin_api_key'
```

After that you can no longer edit the file as your user; rewrite it the same way (the
directory stays yours: `rm` the file, recreate it, chown again).

Token files must not end in a newline (use `printf '%s'`). Never put tokens in `.env`.

## 2. Garmin key

Create the Personal API key in the Garmin developer portal and save it as
`secrets/garmin_api_key`. Set its expiry date in `.env`:
`COACHD_GARMIN_KEY_EXPIRES=YYYY-MM-DD`. coachd re-exchanges the key for a 24 h token daily.
The key stays server-side only.

**Renewing:** create a new key on Garmin's page, then open **Garmin key** on coachd's page (linked
from the home page, which warns 14 days before expiry), paste it and choose the validity you chose
on Garmin. coachd checks it with Garmin and switches at once, without a restart. It stores the
renewed key as `garmin-key.json` (mode 0600) in the data volume and uses whichever key expires
later: this one or the one in `secrets/garmin_api_key` with `COACHD_GARMIN_KEY_EXPIRES`. Backups do
not include it, so after restoring on another host the secret file's key applies until you renew
again.

## 3. Cloudflare (public lane)

1. Host cloudflared: in the Zero Trust dashboard, add a public hostname for the tunnel:
   `coachd.<domain>` -> `http://127.0.0.1:8080` (preferred: avoids a pointless `::1` attempt; `http://localhost:8080` also works).
2. Create an Access application (**Access controls → Applications → Add → Self-hosted**) for
   the whole hostname (path empty). Session duration 24 h is fine.
3. Policy: Allow, Include **Emails** with your email only. Require MFA: either at your identity
   provider (for example Google with 2-step verification), or with Cloudflare's own MFA: tick
   Biometrics and Security key under **Access controls → Access settings → Allow MFA**, require it
   on this application, turn on the **App Launcher** (Access settings → Manage your App Launcher,
   same email-only policy) and register Touch ID or a key at `https://<team>.cloudflareaccess.com/AddMfaDevice`.
   The built-in One-time PIN login method is enough as the first factor.
4. On the application, **Advanced settings → Managed OAuth: on**, then **Save**:
   - **Allow localhost clients** and **Allow loopback clients**: on (Claude Code and other local agents).
   - **Allowed redirect URIs**: `https://claude.ai/api/mcp/auth_callback` and
     `https://claude.com/api/mcp/auth_callback`, plus `https://chatgpt.com/connector_platform_oauth_redirect`
     for ChatGPT. Without them claude.ai fails with "Couldn't register with … sign-in service".
   - Access token lifetime 15 min, grant session 7 days.
5. Put the Access team domain (Settings → Team name and domain, `<team>.cloudflareaccess.com`),
   the application's **Application Audience (AUD) Tag** (on the application's overview) and your email in `.env`:
   `COACHD_ACCESS_TEAM_DOMAIN`, `COACHD_ACCESS_AUD`, `COACHD_ACCESS_ALLOWED_EMAIL`
   (set all three or none), plus `COACHD_PUBLIC_URL=https://coachd.<domain>` (required with
   them), then `docker compose up -d coachd`.

Check: a browser opening `https://coachd.<domain>/` lands on the Access login, and
`curl -X POST https://coachd.<domain>/mcp` answers 401 with a `WWW-Authenticate: Bearer … resource_metadata=…`
header (that header means Managed OAuth is on). If Bot Fight Mode is on for the zone, it can
challenge Anthropic's (`160.79.104.0/21`) and OpenAI's servers; see section 9.

The Access application must cover the **whole hostname** (not just `/mcp`): the runner page
is served at `/` on the same public listener. `COACHD_PUBLIC_URL` must match that hostname
exactly, because it is the WebAuthn relying-party origin. (The watch app's `/device/v1` path
gets its own separate Bypass application, section 10; never a Bypass policy on this one.)

Managed OAuth agents get a `Cf-Access-Jwt-Assertion` for the same Access identity as you, so
the Access login alone cannot tell you from an agent. That is why every runner write on the
page (approve, decline, withdraw, FIT download, notes, passkey changes) also needs a passkey.
GET pages need Access only; the page never changes state on GET.

Enrol your first passkey after the stack is up (Access must already be working). First sign
in at `<COACHD_PUBLIC_URL>/` through Access, in the browser you will enrol from. Then run:

```bash
docker compose exec coachd /coachd enroll
```

It prints a link valid for 10 minutes; open it in that signed-in browser. The token is in the
URL fragment, so it never reaches Cloudflare or server logs, but for the same reason Access's
login redirect drops it: a link opened while signed out lands on the enrol page without its
token, and the page asks you to open the enrolment link again.

Until these are set (or builtin mode is on, section 9), the public lane is disabled: cloudflared
gets connection refused (502), which is safe. coachd validates `Cf-Access-Jwt-Assertion` on every request.

## 4. Tailscale (private lane)

1. Admin console: enable MagicDNS and HTTPS certificates. In the ACL add
   `tagOwners: {"tag:coachd": ["autogroup:admin"]}` and a grant from `autogroup:member`
   to `tag:coachd` on `tcp:443`.
2. Create a one-off, pre-approved, non-ephemeral auth key tagged `tag:coachd` and save it
   as `secrets/tailscale_authkey`, then chown it `0:0` mode `0400` with the recipe above (container root reads it as owner; no capability is added). **Funnel stays off**: `tailscale/serve.json` has
   no `AllowFunnel`, and the container advertises only `tag:coachd` (no routes, no exit node).
3. `docker compose up -d tailscale`. It serves `https://raspberrypi-coachd.<tailnet>.ts.net`
   -> `http://coachd:8081`. Userspace networking: no `/dev/net/tun`, no `NET_ADMIN`.
4. The container sets `TS_AUTH_ONCE=true`: the key is used only for the first join. After the
   first successful join, **revoke the auth key in the admin console and truncate the file**
   (`docker run ... -c ': > /s/tailscale_authkey'`, same recipe). Restarts use the
   `tailscale-state` volume. Do not delete the file: compose still mounts it, and a missing
   file would stop `up`.
5. Create an agent token:
   `docker compose exec coachd /coachd token create --name claude-code --scope read --ttl 90d`

## 5. Grafana Cloud (optional)

Off by default: Alloy keeps metrics and logs local, drops traces, and runs no profiling;
nothing needs cloud credentials. To enable, create the three token files (table above), fill
the `GRAFANA_*` variables in `.env`, and add the override:

```bash
docker compose -f compose.yaml -f compose.cloud.yaml up -d
```

Make it the single switch by putting `COMPOSE_FILE=compose.yaml:compose.cloud.yaml` in `.env`
(see `.env.example`), so a bare `docker compose up -d` does not silently revert to cloud-off.
The override swaps `cloud-off.alloy` for `cloud-on.alloy` (traces to Grafana Cloud OTLP,
15 s Pyroscope scrape of `coachd:9090`), adds the token secrets and the `pdc-agent`.
To disable again, remove that line (or the `-f`) and run `docker compose up -d --remove-orphans` to stop `pdc-agent`.

`pdc-agent` only accepts its token as a CLI flag. The entrypoint reads it from the secret
file at start, so it is not in the compose files or in `docker inspect`, but the token is in
the process argv: visible to every user on the host via `ps` / `/proc/<pid>/cmdline`, as well
as inside the container. Treat the host as trusted, and rotate the token if that changes.

The agent sits on a dedicated `pdc` network (internal) shared only with VictoriaMetrics and
VictoriaLogs, plus its own `pdc-egress` network for Grafana Cloud. Every service that needs the
internet has its own egress network (`coachd-egress`, `ts-egress`, `alloy-egress`, `pdc-egress`),
so no two services share one and an egress network is never a path between containers.
(`-permit-domains` exists but is honoured only with `-use-gossh`, which is not used here; the
network is the restriction.) So neither the agent nor a Grafana Cloud data source routed through
PDC can reach coachd (8080, 8081, 9090), Alloy (4318, 12345) or Tailscale. Remaining exposure:
through `pdc-egress` the agent can still reach the internet, the LAN and the host's addresses, as
any container with outbound access can.

In Grafana Cloud, add data sources via the PDC network:
- Prometheus -> `http://victoriametrics:8428`
- VictoriaLogs plugin -> `http://victorialogs:9428`
- Tempo trace-to-logs custom query: `trace_id:="${__trace.traceId}"`

## 6. Local monitoring (optional)

`compose.local.yaml` adds a self-hosted Grafana (OSS 12.4) with the repo's dashboard
(`grafana/dashboards/coachd.json`), the six alert rules (`grafana/alerts/coachd-alerts.yaml`) and
both data sources (VictoriaMetrics `coachd-vm`, VictoriaLogs `coachd-vl`) provisioned from files,
read-only in the UI. No host port is published: only the Tailscale node reaches it, at
`https://raspberrypi-coachd.<tailnet>.ts.net/grafana/`, on the same `tag:coachd` grant on
`tcp:443` as the MCP lane, so the ACL does not change. The override swaps the tailscale serve
config for `tailscale/serve-local.json`: `serve.json` plus a `/grafana/` handler, still no Funnel.

Grafana runs hardened like the rest (uid 472, read-only rootfs, no capabilities,
`no-new-privileges`, a 16 MB tmpfs at `/tmp` for the assembled provisioning) and adds 256 MB to
the memory budget (`mem_limit: 256m`). Networks: `grafana-data` (internal, shared only with
VictoriaMetrics and VictoriaLogs), `grafana-ui` (internal, shared only with tailscale) and its own
`grafana-egress` (plugin download, ntfy). Grafana is not on `internal`, so it cannot reach coachd
or Alloy; tailscale never joins `grafana-data`.

Secrets (table in section 1), both owned `472:472`, mode `0400`:

- `grafana_admin_password`: the `admin` password. Create it without echoing it.
- `grafana_alert_url`: the full ntfy topic URL for alert notifications. It is a secret (the topic
  is a bearer secret). The file must exist; empty means no notifications (alerts still show in
  Grafana). Append `?template=grafana` so ntfy renders Grafana's webhook JSON as a readable
  message, and check that your ntfy server supports templates by sending a test from Grafana
  (Alerting > Contact points > ntfy > Test). For a protected topic add ntfy's `auth` query
  parameter: raw base64 of the `Authorization` header value, e.g.
  `read -rs NTFY_TOKEN; printf 'Bearer %s' "$NTFY_TOKEN" | base64 -w0 | tr -d '='` (ntfy docs,
  "Query param"). The file must hold one `https://` URL on one line, without quotes,
  backslashes, pipes, spaces or `$` (Grafana expands `$VAR` in provisioned values, so a `$`
  could pull its environment, the admin password included, into the URL); anything else stops
  Grafana at start with an error.

```bash
umask 077
openssl rand -base64 24 > secrets/grafana_admin_password
: > secrets/grafana_alert_url        # empty: notifications off; or, with the URL:
# read -rs ALERT_URL && printf '%s' "$ALERT_URL" > secrets/grafana_alert_url
docker run --rm --network none -v "$PWD/secrets:/s" --entrypoint sh \
  tailscale/tailscale:v1.104.2@sha256:b11583ad2e93e4671128538c88cf11f460444a5d7e449311dd1933a400ab849d \
  -c 'cd /s && chown 472:472 grafana_admin_password grafana_alert_url && chmod 0400 grafana_admin_password grafana_alert_url'
```

Type `https://<ntfy host>/<secret topic>?template=grafana` at the hidden `read -rs` prompt, so
it stays out of shell history (or leave the file empty). The URL reaches Grafana only through the
secret file: the entrypoint (`grafana/local/entrypoint.sh`) writes it into the contact point in
the container's tmpfs using shell builtins only, so it is in no process's argv, the compose files,
the environment or `docker inspect`. Grafana stores it in its
database (`grafana-data` volume) and writes it as `<redacted>` in delivery errors, though a DNS
failure still names the ntfy host (not the topic).

Setup:

1. Set `GRAFANA_ROOT_URL=https://raspberrypi-coachd.<tailnet>.ts.net/grafana/` in `.env`.
2. Enable the override, alone or with cloud: `COMPOSE_FILE=compose.yaml:compose.local.yaml` or
   `COMPOSE_FILE=compose.yaml:compose.cloud.yaml:compose.local.yaml` in `.env`, then
   `docker compose up -d` (this also recreates tailscale with `serve-local.json`).
3. Open `https://raspberrypi-coachd.<tailnet>.ts.net/grafana/` and sign in as `admin`. To read
   the password once (into your password manager):
   `docker compose exec grafana cat /run/secrets/grafana_admin_password`.

Grafana sets the admin password from the file only when it first creates its database; later
changes to the file have no effect. Change it in the UI (profile > Change password), or reset the
database as below. After changing `grafana_alert_url` (rewrite it as in section 1), run
`docker compose up -d --force-recreate grafana`: the entrypoint reads it only at start. A new URL
replaces the old one in the ntfy contact point. To turn notifications off, empty the file and run
the same command. The notification policy tree is file-managed: each start with a URL sets it to
route coachd alerts to ntfy, and **each start with an empty URL resets it to Grafana's default**
and deletes the ntfy contact point, so policy changes made in the UI do not survive a restart.
Fallback, if Grafana's database ever gets out of step with the files: reset it (everything in it
is provisioned from files again, the admin password is re-read from its file and the plugin is
downloaded again):

```bash
docker compose rm -sf grafana
docker volume rm coachd_grafana-data
docker compose up -d grafana
```

To disable local monitoring, drop `compose.local.yaml` from `COMPOSE_FILE` and run
`docker compose up -d --remove-orphans` (tailscale goes back to `serve.json`; the
`grafana-data` volume stays until you remove it).

Limits:

- Traces and profiles stay Grafana Cloud only (section 5); locally the dashboard's traces panel
  is empty (there is no Tempo data source).
- The first start downloads the VictoriaLogs plugin (pinned version, about 80 MB; about 215 MB
  unpacked in `grafana-data`, as the package carries every platform's binary) from grafana.com,
  so the first healthy start takes longer. Plugin signatures are checked with the key built into
  Grafana (no periodic key download), and plugin installs from the UI catalog are off
  (`plugin_admin_enabled=false`; Grafana's `/api/gnet` proxy to grafana.com still answers a
  signed-in user's request). Update checks, usage reporting, the news feed, Gravatar and external
  snapshots are off, and the four Drilldown apps Grafana 12 would preinstall are disabled.
  Nothing else leaves the host on its own, apart from alert notifications to your ntfy server.
- Remaining exposure: through `grafana-egress`, Grafana (and any data source an admin creates)
  can still reach the internet, the LAN and the host's addresses, as any container with outbound
  access can.

## 7. Encrypted `/data`

`/data` (the `coachd-data` volume) holds health data, the Garmin raw JSON and FIT files, and
token hashes. It **must be on an encrypted disk**. A plain SD card is not encrypted: put
Docker's data root (or just this volume) on a LUKS-encrypted USB drive/SSD, or use LUKS on the
root. Unlocking at boot is up to you; coachd does not manage disk encryption.

## 8. ntfy (optional phone notification)

Set `COACHD_NTFY_URL` (full topic URL) in `.env`. Use a topic with `deny-all` default access
and an access token for coachd; the notification body carries only the date, never workout or
health data. The token goes in `secrets/ntfy_token`. The file **must exist** because compose
mounts it as a secret, even with ntfy unused; it may be empty (a public topic, or ntfy unused).
Create it in **one** of two ways, not both.

Either empty (no token):

```bash
umask 077
: > secrets/ntfy_token
```

Or holding the real token (`printf '%s'`: no trailing newline):

```bash
umask 077
read -rs NTFY_TOKEN                     # hidden prompt: stays out of shell history
printf '%s' "$NTFY_TOKEN" > secrets/ntfy_token
```

Then, either way, hand it to coachd (same ownership and mode as `garmin_api_key`):

```bash
docker run --rm --network none -v "$PWD/secrets:/s" --entrypoint sh \
  tailscale/tailscale:v1.104.2@sha256:b11583ad2e93e4671128538c88cf11f460444a5d7e449311dd1933a400ab849d \
  -c 'chown 65532:65532 /s/ntfy_token && chmod 0400 /s/ntfy_token'
```

coachd reads the file only when `COACHD_NTFY_URL` is set; with ntfy unused it is still mounted,
so it must exist.

To change the token later (rotating it, or filling an empty file): after the chown the file
belongs to uid 65532, so writing to it as your user fails. Remove it first (the directory stays
yours), recreate it with the `printf` line above and run the same `docker run` chown step again,
or rewrite it with `sudo`. Then `docker compose up -d --force-recreate coachd`: coachd reads the
token only at start.

`COACHD_NTFY_URL` requires the public lane with the page (Access mode, section 3, or builtin mode,
section 9) and `COACHD_PUBLIC_URL`; the notification's "Review" action opens
`<COACHD_PUBLIC_URL>/p/<id>`.

## 9. Built-in auth (no Cloudflare Access)

In builtin mode coachd does its own login and OAuth instead of Cloudflare Access. You sign in to
the page with a passkey, and an MCP client (claude.ai, Claude Code, ChatGPT, or any client that
follows the MCP authorization spec) gets a token only after you approve it on coachd's consent
page with your passkey. Every page needs a passkey session; an app's token works only on `/mcp`.
The private lane (section 4), the watch API (section 10) and the one published port
(`127.0.0.1:8080`) do not change, and the public `/mcp` never accepts a `cdt_` token.

1. Settings in `.env`: `COACHD_PUBLIC_AUTH=builtin` and `COACHD_PUBLIC_URL=https://coachd.<domain>`,
   with the three `COACHD_ACCESS_*` settings empty (coachd refuses to start otherwise).
   `COACHD_PUBLIC_URL` must be the exact public origin, with its port if not 443: every OAuth URL
   coachd hands out is built from it (never from the request's `Host` or `X-Forwarded-*`), and it
   is the WebAuthn origin. `http://localhost[:port]` is accepted for development only (coachd logs
   a warning); `http://127.0.0.1` is refused, because WebAuthn does not accept an IP address.
   Optionally `COACHD_OAUTH_DCR=on` (see below). Then `docker compose up -d coachd`.
2. Expose `127.0.0.1:8080` over HTTPS, one of two ways:
   - **Cloudflare Tunnel without an Access application.** The easiest: the host cloudflared and
     the public hostname of section 3, step 1, and no Access application (skip steps 2 to 4).
     Cloudflare still sees the traffic decrypted at its edge (your data and the tokens). Client-document
     (CIMD) fetches go out from the coachd host itself, not through the Tunnel, and only when the
     signed-in runner opens an authorize link. Bot Fight
     Mode and WAF challenges must not apply to `/mcp`, `/oauth/*` and `/.well-known/*`: claude.ai
     reaches coachd from Anthropic's servers (`160.79.104.0/21`) and ChatGPT from OpenAI's, and a
     server cannot solve a challenge. Bot Fight Mode (Free plan) cannot be skipped per path, so
     turn it off for the zone; Super Bot Fight Mode and WAF rules can be skipped with a custom rule
     (action Skip) matching those paths.
   - **Caddy on the host**, encrypted end to end: a DNS record for `coachd.<domain>` pointing at
     your public IP, a router port forward of TCP 443 to the host, and a Caddyfile with

     ```
     coachd.<domain> {
         reverse_proxy 127.0.0.1:8080
     }
     ```

     Caddy gets and renews the certificate itself (automatic HTTPS; with only 443 forwarded it
     uses the TLS-ALPN challenge). No proxy headers need configuring.
3. Enrol your first passkey: `docker compose exec coachd /coachd enroll`, and open the printed
   link (valid 10 minutes) in the browser you will use. There is no Access login first; the
   enrolment signs you in. Later you sign in at `<COACHD_PUBLIC_URL>/login` with the passkey.
4. Connect Claude with the URL `https://coachd.<domain>/mcp`: on claude.ai as a custom connector
   (Settings > Connectors), in Claude Code with
   `claude mcp add --transport http coachd https://coachd.<domain>/mcp` and then `/mcp` in a
   session. The app sends you to coachd's sign-in and then the consent page: check the app's web
   address (for Claude, `claude.ai`), keep or clear the write permission, and approve with your
   passkey. ChatGPT takes the same URL in its connector settings. The page's Connected apps
   (`/apps`) lists every connection with a Revoke button that takes effect at once. With ntfy on
   (section 8), coachd tells you when an app connects, and when it revokes a connection because a
   token was reused. The dashboard's OAuth panel charts the requests by outcome.

**Switching from Access mode.** In the Zero Trust dashboard, delete the Access application for the
hostname, or Access keeps answering before coachd; keep the tunnel's public hostname. (The
separate `/device/v1` Bypass application of section 10 is then no longer needed.) Empty the three
`COACHD_ACCESS_*` settings, set the two above and `docker compose up -d coachd`. Your passkeys keep
working, as they are bound to `COACHD_PUBLIC_URL`, which does not change. Reconnect each app
(Access's Managed OAuth tokens mean nothing to coachd). Going back to Access mode deletes every
OAuth grant at startup, so switching to builtin again starts with no connected apps.

**`COACHD_OAUTH_DCR`.** claude.ai, Claude Code and ChatGPT identify themselves with a client
metadata document: their `client_id` is an `https` URL, which coachd fetches (only while you are
signed in), so the consent page shows the app's real web address. Some other MCP clients can only
register themselves with dynamic client registration (RFC 7591); `COACHD_OAUTH_DCR=on` lets them.
Registration is then open to anyone and stores nothing, and their consent page says the app's
identity is not checked and asks you to confirm you recognise where it sends you. It is off by
default because open registration is the easiest way to phish a consent: anyone can register an
app under any name. Turn it on only for a client that needs it. Turning it off suspends apps
already connected through DCR. Turning it on again brings them back, so first revoke on `/apps`
any connection you don't want back.

## 10. Watch app (M3)

The Connect IQ watch app fetches approved workouts from coachd and acknowledges them. Install it
from the Connect IQ Store and set its coachd URL as described in `watch/README.md`; building and
sideloading it yourself is in that file's Development section.

1. Set `COACHD_DEVICE_API=on` in `.env` (default off; any value other than `on` or `off` is an
   error), then `docker compose up -d coachd`. coachd then serves `/device/v1/*` on the public
   listener, **outside** Cloudflare Access: the watch cannot follow an Access login, so the
   device token is the only credential, and coachd checks it on every request. With neither
   Access (section 3) nor builtin auth (section 9) set up, the public listener still binds but
   serves only `/device/v1/*` (everything else is 404). The private lane never serves it. Per device the rate
   limit is 60 requests per minute with a burst of 70 (a first sync makes 1+2N requests, N up to 30).
   The open pairing endpoints have global limits shared by every client: starting a pairing
   10 per minute (burst 5), polling one 60 per minute (burst 30, enough for two watches polling
   every 3 s). An answer over a limit is 429 with `Retry-After: 60`.
2. Cloudflare Access is optional for the device path while no Access application exists. Once
   one does (section 3), create a **separate** self-hosted Access application for the device
   path only: domain `coachd.<domain>`, path `device/v1` (it covers `/device/v1/*`), with a
   single **Bypass** policy (Include: Everyone). Cloudflare applies the more specific
   path-scoped application to `/device/v1/*` and the main application to everything else.
   Never add a Bypass policy to the main coachd application: Access policies have no path
   condition, so a Bypass there turns Access off for the whole hostname. coachd still fails
   closed without a valid `Cf-Access-Jwt-Assertion`, so that mistake breaks MCP and the page
   (401) rather than exposing them. Without the device application, Access redirects the watch
   to a login page and the watch gets error -1002. A Cloudflare service-token header is not
   used, because its secret would be embedded in the app.

   Optional: so internet scanners on the open pairing path cannot use up the global buckets,
   add a Cloudflare rate-limiting rule (Security > WAF > Rate limiting rules): match hostname
   `coachd.<domain>` and URI path starting with `/device/v1/pairings`, count per IP, for
   example more than 10 requests in 10 seconds (one watch polls 3 to 4 times in 10 seconds),
   action Block for the timeout your plan allows.
3. Pair: install coachd from the Connect IQ Store and, in the Connect IQ phone app, set its
   coachd URL to `COACHD_PUBLIC_URL`. Open the app on the watch: it shows your host and a 6-digit
   code (valid 10 minutes). Confirm it on the page at `<COACHD_PUBLIC_URL>/devices` (passkey
   session), or from a host shell:

   ```bash
   docker compose exec coachd /coachd device pair <code> --label fenix
   docker compose exec coachd /coachd device list
   docker compose exec coachd /coachd device revoke <id>
   ```

   The watch receives its token on its next poll and keeps it.
4. Revoke a lost or replaced watch on the Devices page or with `device revoke <id>`; its token
   stops working at once.
5. Delivery: the watch syncs in the foreground (on launch and "Sync now"). A workout fetched
   but not acknowledged within 15 minutes becomes `delivery_failed`, and the Grafana alert
   `WorkoutDeliveryFailed` fires; the next sync retries it. The FIT download on the approval
   page stays available as a fallback (it marks the workout `delivered`). Pick one path per
   workout: a FIT file copied over USB is a separate workout on the watch, and the app still
   downloads its own copy, so the watch ends up with two. When a workout is withdrawn, the app
   removes its own copy on the next sync; a USB copy has to be deleted by hand.

## 11. Backups and restore

**Automatic.** `serve` backs up the database at start and then whenever the newest backup is
24 h old or more (checked hourly). It keeps the newest 7 in `/data/backups` as
`coachd-<UTC YYYYMMDDThhmmssZ>.db` plus a `.json` manifest (SHA-256 of the database and of every
file under `/data/fit`). The Grafana alert `BackupStale` fires when the newest backup is older
than 48 h or no backup metric exists. A newest backup named more than 1 h in the future (the
clock was wrong when it was written) reports no age, so the alert fires, and `serve` backs up at
every check until you fix the clock and delete that backup. Meanwhile those hourly backups push
the older daily ones out of the 7 kept (the off-site restic copy still has them), so fix it soon.

**Manual.** `docker compose exec coachd /coachd backup` prints
`backup written: <path> (database <N> bytes, <M> FIT files, <K> old backups removed)`.

**Not included.**
- `nonce.key` is recreated on start. Outstanding approval and login links stop working.
- `deploy/secrets/` and `.env`. Keep them in a password manager.

**Restore.**

```bash
docker compose stop coachd
docker compose run --rm --no-deps coachd restore /data/backups/coachd-<ts>.db
docker compose start coachd
```

`restore` refuses (before touching anything) while `serve` runs on the data directory, and a
corrupt or non-coachd snapshot, a snapshot from a newer schema, one whose manifest hash does not
match, or one with a non-empty `-wal` file next to it. It also refuses, leaving `coachd.db` in
place, when it cannot prepare the snapshot's copy. The previous `coachd.db` (with `-wal` and
`-shm`) moves to `/data/pre-restore-<ts>/`; delete it once satisfied. (If the previous database
is corrupt, SQLite may discard its invalid `-wal` while coachd tries to read it.) It warns when FIT files
listed in the manifest are missing or differ. Do not run other coachd commands (`token`,
`device`, `backup`, `enroll`) while a restore runs: they open the database and could recreate
`-wal`/`-shm` files during the swap.

What a restore keeps: before the swap it carries forward, from the previous `coachd.db`, every
revocation of an agent token, passkey or watch and every used approval nonce, so nothing revoked
after the snapshot works again and no approval link works twice. It prints
`carried forward <n> revocations and <m> used approval nonces from the previous database`. It
also clears runner sign-in sessions and pending enrolments (sign in again) and, in builtin mode,
every app connection (reconnect your apps), and records a `backup_restored` audit row. If the previous database cannot be read (it may be corrupt), the
restore still happens with a warning: check `docker compose exec coachd /coachd token list` and
`device list`, and revoke again anything revoked after the snapshot.

What rolls back to the snapshot's time: proposals and their states (a workout approved after the
snapshot is pending again), notes, the goal, and tokens, passkeys and watches created after the
snapshot (create them again; re-pair such a watch). Audit rows written after the snapshot are not
in the restored database; they stay in `pre-restore-<ts>/coachd.db`.

**Undo a restore.** Stop coachd, then move all three files back from `pre-restore-<ts>/`,
removing the restored ones first so no `-wal` is left beside the other database. The coachd
image has no shell, so use a throwaway root container (the image from section 1; any image
with a shell works):

```bash
docker compose stop coachd
T=tailscale/tailscale:v1.104.2@sha256:b11583ad2e93e4671128538c88cf11f460444a5d7e449311dd1933a400ab849d
docker run --rm --network none -v coachd_coachd-data:/data --entrypoint sh "$T" -c \
  'cd /data && rm -f coachd.db coachd.db-wal coachd.db-shm && mv pre-restore-<ts>/coachd.db* .'
docker compose start coachd
```

**Off-site with restic.** `restic/restic.sh` wraps a pinned `restic/restic` image, mounting the
volume read-only.
1. Choose a repository (S3, B2, SFTP, ...). Create `restic.env` here (git-ignored):
   `RESTIC_REPOSITORY=...` plus the provider credentials, mode `0600` (`restic.sh` warns when
   group or others can read it).
2. Create the repository password without echoing it, owned by root, mode `0400`:
   `read -rs P; printf '%s' "$P" | sudo tee secrets/restic_password >/dev/null; sudo chown 0:0 secrets/restic_password; sudo chmod 0400 secrets/restic_password`
   (or use the throwaway-container recipe from section 1). **Losing this password means losing
   the backups**; keep a copy in your password manager.
3. `./restic/restic.sh init`, then `./restic/restic.sh backup` (also forgets: keep 7 daily, 4
   weekly, 12 monthly) and `./restic/restic.sh snapshots`.
4. Schedule it, for example in root's crontab (the script needs docker and its working directory
   to be `deploy/`):

   ```
   30 3 * * *  cd /home/coachd/coachd/deploy && ./restic/restic.sh backup  >> /var/log/coachd-restic.log 2>&1
   30 4 * * 0  cd /home/coachd/coachd/deploy && ./restic/restic.sh check   >> /var/log/coachd-restic.log 2>&1
   ```

   or a systemd timer with `OnCalendar=*-*-* 03:30:00` running the same command.

Restic copies `/data/backups` and `/data/fit` only, with the backups lock
(`/data/backups/.lock`) and `*.tmp` excluded.

**Disaster recovery** (new or wiped host). `serve` must not start before step 5: on an empty
volume it would write a backup of an empty database, newer than every real one.
1. Deploy the stack files (checkout, `secrets/`, `.env`) and run
   `docker compose run --rm --no-deps coachd version`: it creates and initialises the
   `coachd_coachd-data` volume (owned by 65532) without starting `serve`.
2. `./restic/restic.sh restore-latest /srv/coachd-restore` (an existing host directory). The
   files land under `/srv/coachd-restore/data/`.
3. Copy `fit` and `backups` into the volume with a throwaway root container (`T` as in "Undo a
   restore" above):

   ```bash
   docker run --rm --network none -v coachd_coachd-data:/data \
     -v /srv/coachd-restore/data:/restore:ro --entrypoint sh "$T" -c \
     'cp -a /restore/fit /restore/backups /data/ && chown -R 65532:65532 /data/fit /data/backups'
   ```

4. List the backups: `docker run --rm --network none -v coachd_coachd-data:/data:ro
   --entrypoint ls "$T" -l /data/backups`. If any `coachd-<ts>` there was written after the
   disaster (for example because `serve` started on the empty volume), delete its `.db` and
   `.json` the same way (`--entrypoint rm "$T" /data/backups/coachd-<ts>.db
   /data/backups/coachd-<ts>.json`, without `:ro`) before starting `serve`.
5. Restore the newest snapshot that came from restic:
   `docker compose run --rm --no-deps coachd restore /data/backups/coachd-<ts>.db`, then
   `docker compose up -d`.

## Notes

- `nonce.key` (the HMAC key for approval and login ceremonies) lives in `/data`, is created on
  first start when the public lane is enabled. It is not in `coachd backup` snapshots (section 11);
  losing it only invalidates ceremonies in flight. In builtin mode it also signs consent requests
  and DCR client ids. Losing it means DCR apps (section 9) cannot start new authorizations (their
  client id no longer verifies) and must register again; existing DCR connections keep refreshing
  only while `COACHD_OAUTH_DCR=on`, since with DCR off their grants are refused.
- VictoriaLogs keeps 30 days of logs and is also capped at 5 GiB on disk
  (`-retention.maxDiskSpaceUsageBytes=5GiB` in `deploy/compose.yaml`; older daily partitions are
  dropped first), so a log flood cannot fill the SD card.
- Hardening: every service is `cap_drop: [ALL]`, `no-new-privileges`, read-only rootfs.
  Exceptions: coachd has a 16 MB `noexec` tmpfs at `/tmp` for SQLite temp files; the tailscale
  key file is owned 0:0 so no capability is added; Alloy is `internal`-only unless cloud is on; VictoriaMetrics/Logs run
  as root in their images (they own their named volumes); the local Grafana has a 16 MB tmpfs at `/tmp`
  for its assembled provisioning.
- Networks: `internal` (internal) joins coachd, tailscale, Alloy, VM and VL; `pdc` (internal, cloud
  only) joins pdc-agent, VM and VL; with local monitoring on, `grafana-data` (internal) joins Grafana,
  VM and VL, and `grafana-ui` (internal) joins Grafana and tailscale. Each service with internet access
  has its own egress network: coachd `coachd-egress` (also carries the `127.0.0.1:8080` publish),
  tailscale `ts-egress`, with cloud on Alloy `alloy-egress` and pdc-agent `pdc-egress`, and with local
  monitoring on Grafana `grafana-egress`. Upgrading from the shared `egress`
  network: after `docker compose up -d`, remove the leftover one with `docker network rm coachd_egress`.
- Upgrading from M2a: compose now mounts the `ntfy_token` secret, so `docker compose up -d` fails
  ("bind source path does not exist") until `secrets/ntfy_token` exists, even with ntfy unused.
  Before the first `up` of the new version, create it empty and hand it to coachd as in section 8:
  `umask 077; : > secrets/ntfy_token`, then the `docker run ... chown 65532:65532 ... chmod 0400`
  step.
- Logs: every container uses the `local` log driver, rotated at 10 MB x 3 files.
- Memory limits: coachd 128m (with `GOMEMLIMIT=100MiB`), tailscale 96m, alloy 256m, VM 192m, VL 192m,
  pdc-agent 64m, local grafana 256m.
- Tuning: `COACHD_POLL_INTERVAL`, `COACHD_BACKFILL_DAYS`, `COACHD_BACKFILL_DELAY` and
  `COACHD_SYNC_MIN_INTERVAL` pass through from `.env`; empty or unset means the built-in default
  (see `.env.example`).
- Images are pinned by multi-arch index digest. To upgrade, resolve a new digest with
  `docker buildx imagetools inspect <image>:<tag>` and update both tag and digest.
