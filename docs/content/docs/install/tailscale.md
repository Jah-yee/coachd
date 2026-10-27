---
title: Tailscale
weight: 6
---

The private lane serves `/mcp` to agents on your own devices (Claude Code, Cursor, local models) over your tailnet, with scoped static tokens. It is never on the internet: Tailscale Funnel stays off.

## Set it up

{{% steps %}}

### Prepare the tailnet

In the admin console, enable MagicDNS and HTTPS certificates. In the ACL, add

```json
"tagOwners": {"tag:coachd": ["autogroup:admin"]}
```

and a grant from `autogroup:member` to `tag:coachd` on `tcp:443`.

### Create the auth key

Create a one-off, pre-approved, non-ephemeral auth key tagged `tag:coachd`. Save it as `secrets/tailscale_authkey`, owned `0:0`, mode `0400`, with the recipe in [Secrets](../secrets/#writing-a-secret): container root reads it as owner, and no capability is added.

**Funnel stays off:** `tailscale/serve.json` has no `AllowFunnel`, and the container advertises only `tag:coachd` (no routes, no exit node).

### Start it

```bash
docker compose up -d tailscale
```

It serves `https://raspberrypi-coachd.<tailnet>.ts.net` → `http://coachd:8081`, with userspace networking: no `/dev/net/tun`, no `NET_ADMIN`.

### Retire the auth key

The container sets `TS_AUTH_ONCE=true`: the key is used only for the first join, and restarts use the `tailscale-state` volume. After the first successful join, **revoke the auth key in the admin console and truncate the file** with the same throwaway-container recipe:

```bash
docker run --rm --network none -v "$PWD/secrets:/s" --entrypoint sh \
  tailscale/tailscale:v1.104.2@sha256:b11583ad2e93e4671128538c88cf11f460444a5d7e449311dd1933a400ab849d \
  -c ': > /s/tailscale_authkey'
```

Do not delete the file: compose still mounts it, and a missing file would stop `up`.

### Create an agent token

```bash
docker compose exec coachd /coachd token create --name claude-code --scope read --ttl 90d
```

Then [connect your client](/docs/agents/#private-lane).

{{% /steps %}}
