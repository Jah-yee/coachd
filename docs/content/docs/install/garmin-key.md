---
title: Garmin key
weight: 3
---

coachd reads your data with your own Garmin Connect Personal API key, exchanged daily for a 24 h token. The key never leaves the server: not to agents, logs, telemetry or the page.

{{< callout type="warning" >}}
The key is personal: use it only for your own data, and never host coachd for someone else with it (Garmin's Supplemental Terms §1).
{{< /callout >}}

## Add the key

Create the key in the Garmin developer portal, save it and set its expiry date:

```bash
put_secret garmin_api_key 65532:65532     # the helper from Secrets
```

```bash {filename=".env"}
COACHD_GARMIN_KEY_EXPIRES=YYYY-MM-DD
```

coachd warns on its home page 14 days before the key expires, and the `GarminKeyExpiring` [alert](/docs/operations/monitoring/#alerts) fires at 7.

## Renew the key

Keys last 30 days to a year. Renew on the page, without a restart:

1. Create a new key on Garmin's page.
2. Open **Garmin key** on coachd's page, paste it and choose the validity you chose on Garmin.

coachd checks the key with Garmin and switches at once. It stores it as `garmin-key.json` in the data volume and uses whichever key expires later: that one, or the secret file's. Backups don't include `garmin-key.json`, so after a restore on another host the secret file's key applies until you renew again.
