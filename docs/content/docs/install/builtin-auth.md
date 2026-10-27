---
title: Built-in auth
weight: 5
---

In built-in mode coachd does its own login and OAuth instead of Cloudflare Access. No Cloudflare account is needed.

- You sign in to the page with a passkey. Every page needs a passkey session.
- An MCP client (claude.ai, Claude Code, ChatGPT, or any client that follows the MCP authorization spec) gets a token only after you approve it on coachd's consent page with your passkey. An app's token works only on `/mcp`.
- coachd's OAuth 2.1 server uses PKCE, rotating refresh tokens and reuse detection.

The private lane, the watch API and the one published port (`127.0.0.1:8080`) do not change, and the public `/mcp` never accepts a private-lane `cdt_` token.

## Set it up

{{% steps %}}

### Settings

```bash {filename=".env"}
COACHD_PUBLIC_AUTH=builtin
COACHD_PUBLIC_URL=https://coachd.<domain>
```

Leave the three `COACHD_ACCESS_*` settings empty; coachd refuses to start otherwise.

`COACHD_PUBLIC_URL` must be the exact public origin, with its port if not 443. Every OAuth URL coachd hands out is built from it (never from the request's `Host` or `X-Forwarded-*`), and it is the WebAuthn origin. `http://localhost[:port]` is accepted for development only (coachd logs a warning); `http://127.0.0.1` is refused, because WebAuthn does not accept an IP address.

Optionally set `COACHD_OAUTH_DCR=on` ([see below](#dynamic-client-registration)). Then:

```bash
docker compose up -d coachd
```

### Expose coachd

Expose `127.0.0.1:8080` over HTTPS, one of two ways.

**Cloudflare Tunnel without an Access application.** The easiest: the host cloudflared and the public hostname from [Cloudflare Access](../cloudflare-access/), step 1, and no Access application (skip its other steps).

- Cloudflare still sees the traffic decrypted at its edge (your data and the tokens).
- Client-document (CIMD) fetches go out from the coachd host itself, not through the Tunnel, and only when the signed-in runner opens an authorize link.
- Bot Fight Mode and WAF challenges must not apply to `/mcp`, `/oauth/*` and `/.well-known/*`: claude.ai reaches coachd from Anthropic's servers (`160.79.104.0/21`) and ChatGPT from OpenAI's, and a server cannot solve a challenge. Bot Fight Mode (Free plan) cannot be skipped per path, so turn it off for the zone; Super Bot Fight Mode and WAF rules can be skipped with a custom rule (action Skip) matching those paths.

**Caddy on the host**, encrypted end to end: a DNS record for `coachd.<domain>` pointing at your public IP, a router port forward of TCP 443 to the host, and a Caddyfile with

```text {filename="Caddyfile"}
coachd.<domain> {
    reverse_proxy 127.0.0.1:8080
}
```

Caddy gets and renews the certificate itself (automatic HTTPS; with only 443 forwarded it uses the TLS-ALPN challenge). No proxy headers need configuring.

### Enrol your passkey

```bash
docker compose exec coachd /coachd enroll
```

Open the printed link (valid 10 minutes) in the browser you will use. There is no Access login first; the enrolment signs you in. Later you sign in at `<COACHD_PUBLIC_URL>/login` with the passkey. More in [Enrol a passkey](../passkey/).

### Connect your apps

Connect Claude, ChatGPT or another client with the URL `https://coachd.<domain>/mcp`: see [Connect an agent](/docs/agents/).

{{% /steps %}}

## Connected apps

The page's **Connected apps** (`/apps`) lists every connection with a Revoke button that takes effect at once. With [ntfy](../notifications/) on, coachd tells you when an app connects, and when it revokes a connection because a token was reused. The dashboard's OAuth panel charts the requests by outcome.

## Dynamic client registration

claude.ai, Claude Code and ChatGPT identify themselves with a client metadata document: their `client_id` is an `https` URL, which coachd fetches (only while you are signed in), so the consent page shows the app's real web address.

Some other MCP clients can only register themselves with dynamic client registration (RFC 7591); `COACHD_OAUTH_DCR=on` lets them. Registration is then open to anyone and stores nothing, and their consent page says the app's identity is not checked and asks you to confirm you recognise where it sends you.

{{< callout type="warning" >}}
DCR is off by default because open registration is the easiest way to phish a consent: anyone can register an app under any name. Turn it on only for a client that needs it.
{{< /callout >}}

Turning it off suspends apps already connected through DCR. Turning it on again brings them back, so first revoke on `/apps` any connection you don't want back.

## Switching from Access mode

1. In the Zero Trust dashboard, delete the Access application for the hostname, or Access keeps answering before coachd. Keep the tunnel's public hostname. (The separate `/device/v1` Bypass application for the watch is then no longer needed.)
2. Empty the three `COACHD_ACCESS_*` settings, set the two above and run `docker compose up -d coachd`.
3. Reconnect each app: Access's Managed OAuth tokens mean nothing to coachd.

Your passkeys keep working, as they are bound to `COACHD_PUBLIC_URL`, which does not change. Going back to Access mode deletes every OAuth grant at startup, so switching to built-in again starts with no connected apps.
