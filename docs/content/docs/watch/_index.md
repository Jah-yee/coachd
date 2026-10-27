---
title: Watch app
weight: 5
---

A Connect IQ app for fēnix and Forerunner watches. It copies the workouts you approved onto the watch and confirms delivery to coachd. It shows the next workout, each workout's steps and the agent's reasons, and the messages your agent sends, and it notifies you of new ones. It only reads coachd's device API: it can't approve, change or withdraw anything.

Tested with Android phones; iPhone is not yet verified.

## Server setup

{{% steps %}}

### Turn on the device API

```bash {filename=".env"}
COACHD_DEVICE_API=on
```

Then `docker compose up -d coachd`. coachd serves `/device/v1/*` on the public listener, **outside** Cloudflare Access: the watch can't follow an Access login, so its device token is the only credential, checked on every request.

### Access mode only: add a Bypass application

Create a **separate** self-hosted Access application for the device path only: domain `coachd.<domain>`, path `device/v1`, with a single **Bypass** policy (Include: Everyone). Cloudflare applies this more specific application to `/device/v1/*` and the main one to everything else. Without it, the watch gets Access's login page (error -1002).

{{< callout type="error" >}}
Never add a Bypass policy to the main coachd application: Access policies have no path condition, so it would turn Access off for the whole hostname. (coachd would still refuse requests without a valid Access JWT, so the page and MCP break rather than open up.)
{{< /callout >}}

### Optional: rate-limit pairing

The pairing endpoints are open, with global limits shared by every client. So scanners can't use them up, add a Cloudflare rate-limiting rule on URI paths starting with `/device/v1/pairings`, per IP, for example more than 10 requests in 10 seconds (a watch polls 3 to 4 times in 10 seconds).

{{% /steps %}}

## Install

1. Install **coachd** from the Connect IQ Store.
2. In the Connect IQ app, set coachd's **coachd URL** setting to your `COACHD_PUBLIC_URL`, for example `https://coachd.example.com`. It must be `https://`; a bare host works too.
3. [Pair the watch](pairing/).

{{< cards >}}
  {{< card link="pairing" title="Pairing" icon="link" >}}
  {{< card link="using" title="Using the app" icon="clock" >}}
  {{< card link="status-messages" title="Status messages" icon="exclamation-circle" >}}
  {{< card link="development" title="Development" icon="code" >}}
{{< /cards >}}
