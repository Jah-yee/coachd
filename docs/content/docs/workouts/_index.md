---
title: Approving workouts
linkTitle: Workouts
weight: 4
---

Every workout the agent proposes waits for you. Nothing reaches your watch until you approve it with your passkey.

## The flow

{{% steps %}}

### The agent proposes

The agent calls `propose_workout`. coachd checks it and stores it as `pending`, with warnings such as "longer than 110% of your longest run in the last 30 days".

### Your phone buzzes

If [ntfy](/docs/install/notifications/) is set up, coachd sends a minimal notice: "Workout for Tue 28 Oct ready to review".

### You review it

Open the approval page (behind Cloudflare Access, or signed in with your passkey in built-in mode). It shows the workout's steps, the warnings and the agent's rationale, labelled as agent text.

### You approve, or decline

Approve with your passkey (Face ID or fingerprint). Declining is one tap after a passkey login.

### It reaches the watch

The [watch app](/docs/watch/) picks it up on its next sync and confirms delivery. Without the watch app, download the FIT file from the page and copy it to the watch (`GARMIN/NewFiles`). The first download marks the workout `delivered`.

{{% /steps %}}

![Approval page on a phone: an agent-proposed threshold workout with its steps, the agent's reason, a warning, and the Approve with passkey button](/images/approve.png)

## A week at a time

When several workouts are waiting (a week's plan), **Review all** on the home page shows each one in full and approves all of them with one passkey prompt. It approves exactly the workouts shown, or none if any changed meanwhile. To leave one out, decline it first.

## What happens next

| Status | Meaning |
|---|---|
| `pending` | Waiting for you. A proposal nobody approves expires at the end of its date. |
| `approved` | You approved it; the watch app or a download picks it up. |
| `delivering` | The watch app fetched it and has not confirmed yet. |
| `delivered` | The watch confirmed it, or you downloaded its FIT or Zwift file. |
| `delivery_failed` | No confirmation within 15 minutes; the next sync tries again. |
| `declined` | You declined it. |
| `withdrawn` | You withdrew it after approving, or approved a replacement for it; the watch app removes it on its next sync. |
| `cancelled` | The agent cancelled it before you decided. |
| `superseded` | You approved a replacement while it was still `pending`. |
| `expired` | Its date passed while it was `pending`. |

A proposal never changes after it is stored. The agent changes a plan by proposing a new workout that `replaces` the old one, and you approve that.

## Delivery paths

Pick one path per workout:

- **The watch app** downloads it and confirms. When a workout is withdrawn or replaced, the app removes its own copy on the next sync.
- **The FIT file** from the page, copied over USB. It is a separate workout on the watch: the app still downloads its own copy, so the watch ends up with two, and a USB copy has to be deleted by hand.
- **A Zwift file** for a cycling workout: see [cycling and Zwift](cycling/).

## Planned vs actual

After you run a delivered workout, coachd links the activity to it and records each executed step. The agent sees the planned and executed steps side by side with `get_activity` (detail `laps`), and the activities that carried out each workout with `list_workouts`. See [how it works](/docs/how-it-works/#planned-vs-actual).
