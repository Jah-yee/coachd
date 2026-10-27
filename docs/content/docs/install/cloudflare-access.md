---
title: Cloudflare Access
weight: 4
---

In Access mode, a Cloudflare Tunnel carries the public lane and Cloudflare Access does the login and MFA. Its Managed OAuth signs in the AI apps. coachd validates the `Cf-Access-Jwt-Assertion` on every request.

The alternative without Cloudflare Access is coachd's [built-in auth](../builtin-auth/).

## Set it up

{{% steps %}}

### Route the tunnel to coachd

On the host's cloudflared, in the Zero Trust dashboard, add a public hostname for the tunnel: `coachd.<domain>` → `http://127.0.0.1:8080`. (`http://localhost:8080` also works, but `127.0.0.1` avoids a pointless `::1` attempt.)

### Create the Access application

**Access controls → Applications → Add → Self-hosted**, for the **whole hostname** (path empty). A session duration of 24 h is fine.

The application must cover the whole hostname, not just `/mcp`: the runner page is served at `/` on the same public listener.

### Allow only you, with MFA

Policy: Allow, Include **Emails** with your email only. Require MFA, either:

- at your identity provider (for example Google with 2-step verification), or
- with Cloudflare's own MFA: tick Biometrics and Security key under **Access controls → Access settings → Allow MFA**, require it on this application, turn on the **App Launcher** (Access settings → Manage your App Launcher, same email-only policy) and register Touch ID or a key at `https://<team>.cloudflareaccess.com/AddMfaDevice`.

The built-in One-time PIN login method is enough as the first factor.

### Turn on Managed OAuth

On the application, **Advanced settings → Managed OAuth: on**, then **Save**:

- **Allow localhost clients** and **Allow loopback clients**: on (Claude Code and other local agents).
- **Allowed redirect URIs**: `https://claude.ai/api/mcp/auth_callback` and `https://claude.com/api/mcp/auth_callback`, plus `https://chatgpt.com/connector_platform_oauth_redirect` for ChatGPT. Without them claude.ai fails with "Couldn't register with … sign-in service".
- Access token lifetime 15 min, grant session 7 days.

### Tell coachd

Put the Access team domain (Settings → Team name and domain, `<team>.cloudflareaccess.com`), the application's **Application Audience (AUD) Tag** (on the application's overview) and your email in `.env`, with the public URL:

```bash {filename=".env"}
COACHD_ACCESS_TEAM_DOMAIN=<team>.cloudflareaccess.com
COACHD_ACCESS_AUD=<AUD tag>
COACHD_ACCESS_ALLOWED_EMAIL=you@example.com
COACHD_PUBLIC_URL=https://coachd.<domain>
```

Set all three `COACHD_ACCESS_*` or none. `COACHD_PUBLIC_URL` is required with them and must match the hostname exactly, because it is the WebAuthn relying-party origin. Then:

```bash
docker compose up -d coachd
```

{{% /steps %}}

## Check it

- A browser opening `https://coachd.<domain>/` lands on the Access login.
- `curl -X POST https://coachd.<domain>/mcp` answers 401 with a `WWW-Authenticate: Bearer … resource_metadata=…` header. That header means Managed OAuth is on.

If Bot Fight Mode is on for the zone, it can challenge Anthropic's (`160.79.104.0/21`) and OpenAI's servers, and a server cannot solve a challenge. See the note under [built-in auth](../builtin-auth/#expose-coachd).

Until these settings are in place (or built-in mode is on), the public lane is disabled: cloudflared gets connection refused (502), which is safe.

## Why the page still asks for a passkey

Managed OAuth agents get a `Cf-Access-Jwt-Assertion` for the same Access identity as you, so the Access login alone cannot tell you from an agent. That is why every runner write on the page (approve, decline, withdraw, FIT download, notes, passkey changes) also needs a passkey. GET pages need Access only; the page never changes state on GET.

Next: [enrol your passkey](../passkey/).

{{< callout type="info" >}}
The watch app's `/device/v1` path needs its own, separate Access application with a Bypass policy. Never add a Bypass policy to this one: see [watch app server setup](/docs/watch/#server-setup).
{{< /callout >}}
