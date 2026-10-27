---
title: Using the app
weight: 2
---

The app syncs every time it opens. Everything on its screens comes from what the last sync stored, so it works without the phone.

## Main screen

The next workout (Today, Tomorrow or the weekday, its name, and sport, duration and distance), "N new messages" when the agent sent something you have not read, and the sync status or "Last sync HH:MM" at the bottom.

## Menu

START opens the menu:

- **The workouts** from today on, in date order (the name; the day and duration). Selecting one shows its steps, notes and why the agent proposed it (both labelled as agent text); UP and DOWN scroll. START there opens it in the watch's workout player; the watch asks before it leaves the app. A workout not downloaded yet says "Sync now" instead.
- **Messages** lists the agent's messages from the last 7 days (at most 5), newest first. Opening one marks it and every older one read.
- **Sync now** syncs again.
- **Unpair**: see [unpairing](../pairing/#unpairing).

## What a sync does

1. Fetches the desired set: approved, delivering, delivery_failed and delivered workouts from today to 14 days ahead.
2. Removes the app's workouts that left it (withdrawn, replaced or past).
3. Downloads the missing ones one at a time and acknowledges each, which marks it `delivered` in coachd.
4. Fetches the details of workouts it has none for, and the messages.

The app only ever removes workouts it downloaded itself. With a coachd older than 0.4 there are no details or messages, and the screens show names and dates only.

## Notifications

While paired, the app checks coachd in the background every 15 minutes (through the phone, like a sync). When the agent sent a new message, or a workout you approved is not on the watch yet, the watch tells you: fēnix 7 and later show a notification, older watches ask whether to open coachd.

- One check notifies at most one thing, and never the same thing twice.
- The check never downloads: open the app to sync.
- Turn it off with **Notifications** in coachd's settings in the Connect IQ app.

On fēnix 7 and later, coachd's glance shows the next workout and unread messages.

## One delivery path per workout

A FIT file downloaded from the approval page and copied over USB is a separate workout on the watch, and the app still downloads its own copy, so the watch ends up with two. The app removes only its own copy when a workout leaves the set; a USB copy has to be deleted by hand.
