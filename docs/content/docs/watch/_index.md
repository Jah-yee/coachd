---
title: Watch app
weight: 5
---

A Connect IQ watch app for fēnix and Forerunner watches. It pairs with your coachd once, then copies the workouts you approved onto the watch and tells coachd it has them. It shows the next workout, each workout's steps and the agent's reasons, and the short messages your agent sends with `send_message`, and it notifies you of new ones.

It only reads coachd's device API (`/device/v1/*`): it cannot approve, change or withdraw anything.

Tested with Android phones; iPhone is not yet verified.

## Server setup

{{% steps %}}

### Turn on the device API

```bash {filename=".env"}
COACHD_DEVICE_API=on
```

Then `docker compose up -d coachd`. The default is off; any value other than `on` or `off` is an error.

coachd then serves `/device/v1/*` on the public listener, **outside** Cloudflare Access: the watch cannot follow an Access login, so the device token is the only credential, and coachd checks it on every request. With neither Access nor built-in auth set up, the public listener still binds but serves only `/device/v1/*` (everything else is 404). The private lane never serves it.

### Access mode only: add a Bypass application

Cloudflare Access is optional for the device path while no Access application exists. Once one does, create a **separate** self-hosted Access application for the device path only: domain `coachd.<domain>`, path `device/v1` (it covers `/device/v1/*`), with a single **Bypass** policy (Include: Everyone). Cloudflare applies the more specific path-scoped application to `/device/v1/*` and the main application to everything else.

Without it, Access redirects the watch to a login page and the watch shows error -1002. A Cloudflare service-token header is not used, because its secret would be embedded in the app. In built-in mode there is no Access application, so skip this step.

{{< callout type="error" >}}
Never add a Bypass policy to the main coachd application. Access policies have no path condition, so a Bypass there turns Access off for the whole hostname. coachd still fails closed without a valid `Cf-Access-Jwt-Assertion`, so that mistake breaks MCP and the page (401) rather than exposing them.
{{< /callout >}}

### Optional: rate-limit the pairing path

So internet scanners on the open pairing path cannot use up coachd's global pairing limits, add a Cloudflare rate-limiting rule (Security → WAF → Rate limiting rules): match hostname `coachd.<domain>` and URI path starting with `/device/v1/pairings`, count per IP, for example more than 10 requests in 10 seconds (one watch polls 3 to 4 times in 10 seconds), action Block for the timeout your plan allows.

{{% /steps %}}

### Limits

- Per device: 60 requests per minute with a burst of 70. A first sync makes 1+2N requests, N up to 30.
- The open pairing endpoints have global limits shared by every client: starting a pairing 10 per minute (burst 5), polling one 60 per minute (burst 30, enough for two watches polling every 3 s).
- An answer over a limit is 429 with `Retry-After: 60`.

## Install

1. Install **coachd** from the Connect IQ Store, in the Connect IQ app on your phone or on the web.
2. In the Connect IQ app, open coachd's settings and set **coachd URL** to your coachd's public URL, the same value as `COACHD_PUBLIC_URL` in `.env`, for example `https://coachd.example.com`.
   - It must be `https://`. Surrounding spaces, a capitalised `Https://` and trailing slashes are fine, and a bare host such as `coachd.example.com` means `https://coachd.example.com`.
   - Until a valid URL is set, the watch shows "Set the coachd URL in the Connect IQ app" and makes no request.
3. [Pair the watch](pairing/).

## Delivery

The watch syncs in the foreground, on launch and with **Sync now**. A workout fetched but not acknowledged within 15 minutes becomes `delivery_failed`, and the `WorkoutDeliveryFailed` [alert](/docs/operations/monitoring/#alerts) fires; the next sync retries it. The FIT download on the approval page stays available as a fallback.

{{< cards >}}
  {{< card link="pairing" title="Pairing" icon="link" >}}
  {{< card link="using" title="Using the app" icon="clock" >}}
  {{< card link="status-messages" title="Status messages" icon="exclamation-circle" >}}
  {{< card link="development" title="Development" icon="code" >}}
{{< /cards >}}
