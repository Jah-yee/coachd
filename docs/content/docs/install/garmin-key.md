---
title: Garmin key
weight: 3
---

coachd reads your data with your own Garmin Connect Personal API key. The key stays server-side only: it never reaches agents, logs, telemetry or the page.

{{< callout type="warning" >}}
The key is personal: use it only for your own data, never share it, and never host coachd for someone else with it (Garmin's Supplemental Terms §1).
{{< /callout >}}

## Add the key

1. Create the Personal API key in the Garmin developer portal.
2. Save it as `secrets/garmin_api_key`, owned `65532:65532`, mode `0400`, with the recipe in [Secrets](../secrets/#writing-a-secret).
3. Set its expiry date in `.env`:

   ```bash {filename=".env"}
   COACHD_GARMIN_KEY_EXPIRES=YYYY-MM-DD
   ```

coachd exchanges the key for a 24 h token and re-exchanges it daily. It warns before the key expires: on the home page from 14 days before, and through the `GarminKeyExpiring` [alert](/docs/operations/monitoring/#alerts).

## Renew the key

Garmin keys expire, after 30 days to 1 year. You renew on coachd's page, without a restart:

1. Create a new key on Garmin's page.
2. Open **Garmin key** on coachd's page (linked from the home page).
3. Paste the key and choose the validity you chose on Garmin.

coachd checks the new key with Garmin and switches at once. It stores the renewed key as `garmin-key.json` (mode 0600) in the data volume and uses whichever key expires later: this one, or the one in `secrets/garmin_api_key` with `COACHD_GARMIN_KEY_EXPIRES`.

Backups do not include `garmin-key.json`, so after restoring on another host the secret file's key applies until you renew again.
