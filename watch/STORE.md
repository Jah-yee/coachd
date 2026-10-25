# Connect IQ Store listing

The text to paste into the Connect IQ developer dashboard, and the owner's upload checklist.
Design: [`docs/design/watch-store.md`](../docs/design/watch-store.md).

## Listing

**Name:** coachd

**Short description:** Puts the workouts you approved in your own coachd server onto your watch.

**Description:**

> coachd is a self-hosted gateway between your AI coach and your Garmin watch. Your AI agent
> proposes a workout, you approve it with a passkey on your coachd's page, and this app copies it
> onto your watch, ready to start in the workout player.
>
> **You need your own coachd server.** This app does nothing on its own: it talks only to the
> coachd you run. Set it up first: https://github.com/stefiix92/coachd
>
> **Setup**
> 1. Install coachd on your watch.
> 2. In the Connect IQ app on your phone, open coachd's settings and enter your coachd URL
>    (for example `https://coachd.example.com`).
> 3. Open coachd on the watch: it shows your server's name and a 6-digit code.
> 4. Confirm the code on your coachd's Devices page.
>
> The app syncs when you open it, or with Sync now in its menu. It only reads the workouts you
> approved: it cannot approve, change or withdraw anything.
>
> **Permissions**
> - Communications: talks to your coachd through your phone.
> - Persisted content: stores the workouts on the watch.
>
> **Privacy:** the app talks only to the URL you set and sends nothing to the developer.
>
> Tested with Android phones; iPhone not yet verified.
>
> Not affiliated with or endorsed by Garmin.

**Support link:** https://github.com/stefiix92/coachd/issues

## Upload checklist (owner)

1. Refresh the device list: download any new fēnix and Forerunner device files in the Connect IQ
   SDK Manager, run `./devices.sh`, and update `<iq:products>` in `manifest.xml` if it changed.
2. Build the beta: `./build.sh store-beta` (`bin/coachd-beta.iq`, app "coachd dev").
3. On the developer dashboard, upload it as a new app with **Beta App** ticked. Enter a version
   higher than the last upload: the manifest carries none.
4. Install it from the beta link with your own Garmin account, then run the real-watch checks:
   - set the coachd URL in the Connect IQ phone app; the watch shows the host and a code;
     confirm it on the Devices page; the sync runs;
   - approve a test workout, then Sync now: it shows as `delivered` in coachd;
   - change the URL to a wrong host: the watch forgets the pairing and shows pairing for the new
     host; set it back, pair again, then revoke the stale device on the Devices page;
   - look at the setup, pairing and status screens for clipped text.
5. Only after you approve the beta: build the release with `./build.sh store` (`bin/coachd.iq`,
   app "coachd") and upload it as the public app with this listing.
6. After the iPhone test, update the "iPhone not yet verified" line here and in the listing.
