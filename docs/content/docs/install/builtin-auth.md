---
title: Built-in auth
weight: 5
---

coachd does its own login and OAuth instead of Cloudflare Access. You sign in to the page with your passkey; an AI app gets a token only after you approve it on coachd's consent page with your passkey. coachd's OAuth 2.1 server uses PKCE, rotating refresh tokens and reuse detection, and an app's token works only on `/mcp`.

{{% steps %}}

### Settings

```bash {filename=".env"}
COACHD_PUBLIC_AUTH=builtin
COACHD_PUBLIC_URL=https://coachd.<domain>
```

Leave the `COACHD_ACCESS_*` settings empty, or coachd refuses to start. `COACHD_PUBLIC_URL` must be the exact public origin, with its port if not 443: every OAuth URL is built from it (never from request headers), and it is the WebAuthn origin. For development, `http://localhost[:port]` is accepted; an IP address is not. Then `docker compose up -d coachd`.

### Expose coachd

Serve `127.0.0.1:8080` over HTTPS, one of two ways.

**Cloudflare Tunnel, without an Access application.** The easiest: route the tunnel as in [Cloudflare Access](../cloudflare-access/), step 1, and stop there. Cloudflare still sees the traffic decrypted at its edge.

{{< callout type="warning" >}}
claude.ai and ChatGPT reach coachd from their servers (Anthropic's `160.79.104.0/21`), and a server cannot solve a challenge. Keep Bot Fight Mode and WAF challenges off `/mcp`, `/oauth/*` and `/.well-known/*`. Bot Fight Mode (Free plan) can't be skipped per path, so turn it off for the zone; Super Bot Fight Mode and WAF rules can be skipped with a custom rule.
{{< /callout >}}

**Caddy on the host**, encrypted end to end: a DNS record pointing at your public IP, TCP 443 forwarded to the host, and

```text {filename="Caddyfile"}
coachd.<domain> {
    reverse_proxy 127.0.0.1:8080
}
```

Caddy gets and renews the certificate itself.

### Enrol your passkey

`docker compose exec coachd /coachd enroll`, and open the link: see [Enrol a passkey](../passkey/). Later you sign in at `<COACHD_PUBLIC_URL>/login`.

### Connect your apps

With `https://coachd.<domain>/mcp`: see [Connect an agent](/docs/agents/).

{{% /steps %}}

## Connected apps

**Connected apps** (`/apps`) lists every connection, each with a Revoke button that takes effect at once. With [ntfy](../notifications/) on, coachd tells you when an app connects, and when it revokes a connection because a token was reused.

## Dynamic client registration

claude.ai, Claude Code and ChatGPT identify themselves with a client metadata document (their `client_id` is an `https` URL), so the consent page shows their real web address. A client that can only register itself (RFC 7591) needs `COACHD_OAUTH_DCR=on`; its consent page then warns that its identity is not checked.

{{< callout type="warning" >}}
DCR is off by default: open registration is the easiest way to phish a consent, since anyone can register an app under any name. Turn it on only for a client that needs it.
{{< /callout >}}

Turning DCR off suspends apps connected through it; turning it on again brings them back, so revoke any you don't want first.

## Switching from Access mode

1. Delete the Access application for the hostname (keep the tunnel's public hostname), or Access keeps answering before coachd. The watch's `/device/v1` Bypass application is no longer needed.
2. Empty the `COACHD_ACCESS_*` settings, set the two above, and `docker compose up -d coachd`.
3. Reconnect each app: Access's tokens mean nothing to coachd.

Your passkeys keep working, since `COACHD_PUBLIC_URL` doesn't change. Switching back to Access mode deletes every OAuth grant.
