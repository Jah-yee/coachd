---
title: Cloudflare Access
weight: 4
---

A Cloudflare Tunnel carries the public lane, and Cloudflare Access does the login and MFA. Its Managed OAuth signs in the AI apps. coachd validates the `Cf-Access-Jwt-Assertion` on every request. Prefer no Cloudflare? Use [built-in auth](../builtin-auth/).

{{% steps %}}

### Route the tunnel to coachd

In the Zero Trust dashboard, add a public hostname to the host's tunnel: `coachd.<domain>` → `http://127.0.0.1:8080`.

### Create the Access application

**Access controls → Applications → Add → Self-hosted**, for the **whole hostname** (path empty), not just `/mcp`: the runner page is served at `/`. A 24 h session is fine.

### Allow only you, with MFA

Policy: Allow, Include **Emails** with your email only. Require MFA, either at your identity provider, or with Cloudflare's own: tick Biometrics and Security key under **Access settings → Allow MFA**, require it on this application, turn on the **App Launcher** with the same policy, and register Touch ID or a key at `https://<team>.cloudflareaccess.com/AddMfaDevice`. One-time PIN is enough as the first factor.

### Turn on Managed OAuth

On the application, **Advanced settings → Managed OAuth: on**, then **Save**:

- **Allow localhost clients** and **Allow loopback clients**: on, for Claude Code and other local agents.
- **Allowed redirect URIs**: `https://claude.ai/api/mcp/auth_callback`, `https://claude.com/api/mcp/auth_callback` and, for ChatGPT, `https://chatgpt.com/connector_platform_oauth_redirect`. Without them claude.ai fails with "Couldn't register with … sign-in service".
- Access token lifetime 15 min, grant session 7 days.

### Tell coachd

```bash {filename=".env"}
COACHD_ACCESS_TEAM_DOMAIN=<team>.cloudflareaccess.com   # Settings → Team name and domain
COACHD_ACCESS_AUD=<AUD tag>                             # on the application's overview
COACHD_ACCESS_ALLOWED_EMAIL=you@example.com
COACHD_PUBLIC_URL=https://coachd.<domain>
```

`COACHD_PUBLIC_URL` must match the hostname exactly: it is the WebAuthn origin your passkeys are bound to. Then `docker compose up -d coachd`.

{{% /steps %}}

## Check it

- `https://coachd.<domain>/` lands on the Access login.
- `curl -X POST https://coachd.<domain>/mcp` answers 401 with `WWW-Authenticate: Bearer … resource_metadata=…`: Managed OAuth is on.

If the zone has Bot Fight Mode on, it can challenge Anthropic's and OpenAI's servers: see [built-in auth](../builtin-auth/#expose-coachd). Until the settings are in place the public lane is off, and the tunnel gets a 502.

{{< callout type="info" >}}
An agent signed in through Managed OAuth carries the same Access identity as you. That's why every write on the page (approve, decline, withdraw, downloads, notes, passkeys) also needs your passkey. Next: [enrol it](../passkey/).
{{< /callout >}}

{{< callout type="warning" >}}
The watch app's `/device/v1` path needs its own Access application with a Bypass policy. Never add a Bypass policy to this one: see [watch app setup](/docs/watch/#server-setup).
{{< /callout >}}
