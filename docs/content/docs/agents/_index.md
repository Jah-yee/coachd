---
title: Connect an agent
weight: 3
---

Your agent talks to coachd over MCP at `/mcp`. coachd serves MCP spec revisions 2026-07-28 and 2025-11-25, so current clients connect without extra settings.

| Lane | URL | Sign-in |
|---|---|---|
| Public | `https://coachd.<domain>/mcp` | OAuth: Cloudflare Access's Managed OAuth, or coachd's own in built-in mode |
| Private | `https://raspberrypi-coachd.<tailnet>.ts.net/mcp` | a static token from `coachd token create` |

## Public lane

{{< tabs >}}
{{< tab name="claude.ai" >}}
**Settings → Connectors → Add custom connector**, with the URL `https://coachd.<domain>/mcp`.
{{< /tab >}}
{{< tab name="Claude Code" >}}
```bash
claude mcp add --transport http coachd https://coachd.<domain>/mcp
```

Then run `/mcp` in a session to sign in.
{{< /tab >}}
{{< tab name="ChatGPT" >}}
Add the URL `https://coachd.<domain>/mcp` in ChatGPT's connector settings.
{{< /tab >}}
{{< tab name="Other clients" >}}
Any client that follows the MCP authorization spec works. A client that cannot identify itself with a client metadata document needs [dynamic client registration](/docs/install/builtin-auth/#dynamic-client-registration) in built-in mode.
{{< /tab >}}
{{< /tabs >}}

The app then sends you to sign in:

- **Access mode:** the Cloudflare Access login, and Access's Managed OAuth issues the app's token. The redirect URIs must be allowed on the Access application ([step 4](/docs/install/cloudflare-access/#turn-on-managed-oauth)).
- **Built-in mode:** coachd's sign-in, then its consent page. Check the app's web address (for Claude, `claude.ai`), keep or clear the write permission, and approve with your passkey. **Connected apps** (`/apps`) on the page lists every connection, with a Revoke button that takes effect at once.

## Private lane

For clients on your own devices, over [Tailscale](/docs/install/tailscale/). Create a token on the host:

```bash
docker compose exec coachd /coachd token create --name claude-code --scope read --ttl 90d
```

It prints the token once. Then, for Claude Code:

```bash
claude mcp add --transport http coachd https://raspberrypi-coachd.<tailnet>.ts.net/mcp \
  --header "Authorization: Bearer <token>"
```

Other clients take the same URL and `Authorization: Bearer` header. Private-lane tokens start with `cdt_` and work only on the private lane.

`token list` shows your tokens and `token revoke <id>` revokes one; see the [CLI reference](/docs/reference/cli/#token).

## Read or write

| Scope | Allows |
|---|---|
| read | every read tool: Garmin data, summaries, goal, notes, workouts |
| write | read, plus coachd's local writes: `request_sync`, `set_goal`, notes, `propose_workout`, `cancel_proposal`, `send_message` |

A `--scope write` token carries both. In built-in mode the consent page sets the scope; in Access mode an app gets both.

Even the write scope cannot approve, deliver or withdraw a workout: only you can, on coachd's page with your passkey. See [MCP tools](tools/).
