---
title: Using the app
weight: 2
---

The app syncs whenever it opens. Its screens show what the last sync stored, so they work without the phone.

- **Main screen:** the next workout (day, name, sport, duration, distance), "N new messages", and the sync status.
- **START** opens the menu:
  - **The workouts** from today on. Select one for its steps, notes and the agent's reasons; START there opens it in the watch's workout player.
  - **Messages** from the agent, last 7 days (at most 5). Opening one marks it and older ones read.
  - **Sync now** and **Unpair**.

## Sync

A sync fetches the approved workouts from today to 14 days ahead, removes the app's workouts that left that set (withdrawn, replaced or past), downloads the missing ones and confirms each, which marks it `delivered`. The app only removes workouts it downloaded itself.

## Notifications

While paired, the app checks coachd every 15 minutes in the background, through the phone. When the agent sent a message, or a workout you approved isn't on the watch yet, it tells you: a notification on fēnix 7 and later, an "open coachd?" prompt on older watches. The check never downloads: open the app to sync. Turn it off with **Notifications** in the app's Connect IQ settings.

On fēnix 7 and later, coachd's glance shows the next workout and unread messages.
