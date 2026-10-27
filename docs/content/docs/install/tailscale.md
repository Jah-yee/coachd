---
title: Tailscale
weight: 6
---

The private lane serves `/mcp` over your tailnet to agents on your own devices, with scoped static tokens. Funnel stays off: it is never on the internet.

{{% steps %}}

### Prepare the tailnet

Enable MagicDNS and HTTPS certificates. In the ACL, add `"tagOwners": {"tag:coachd": ["autogroup:admin"]}` and a grant from `autogroup:member` to `tag:coachd` on `tcp:443`.

### Add the auth key

Create a one-off, pre-approved, non-ephemeral auth key tagged `tag:coachd`:

```bash
put_secret tailscale_authkey 0:0     # the helper from Secrets
docker compose up -d tailscale
```

The node serves `https://raspberrypi-coachd.<tailnet>.ts.net` (the hostname set in `compose.yaml`) → coachd's private listener, with userspace networking and no routes or exit node.

### Retire the auth key

The key is used only for the first join; restarts use the `tailscale-state` volume. Once the node has joined, **revoke the key in the admin console** and empty the file. Don't delete it: compose still mounts it.

```bash
put_secret tailscale_authkey 0:0     # press Enter: empty
```

### Create an agent token

```bash
docker compose exec coachd /coachd token create --name claude-code --scope read --ttl 90d
```

Then [connect your client](/docs/agents/#private-lane).

{{% /steps %}}
