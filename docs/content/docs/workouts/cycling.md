---
title: Cycling and Zwift
weight: 1
---

Cycling workouts can carry power targets, run on a smart trainer through the watch, or download for Zwift.

## Power targets

A cycling or indoor cycling step can target power in **% of FTP** or in **watts** (1 to 1500 W), never both in one step.

- **% of FTP:** coachd does not store your FTP. The FIT file carries the percentage as it is, so the watch converts it with its own FTP when the ride starts, and Zwift with the FTP in your Zwift profile. A changed FTP applies to every workout already on the watch.
- **Watts:** the page and the watch show a watt step as `power 200–220 W`.

## On a smart trainer

The `indoor_cycling` sport is for a ride on a smart trainer. The watch starts it as an indoor ride, so a watch paired with a smart trainer can drive it. It follows every cycling rule, and the page shows it as "indoor cycling".

To ride one: pair the trainer as a sensor on the watch, then start the workout from the coachd watch app. If the watch opens an outdoor ride, or the trainer does not follow the step's power, [open an issue](https://github.com/stefiix92/coachd/issues).

## Zwift workouts

A cycling workout whose steps all have a time duration can be downloaded for Zwift as a `.zwo` file. Power targets in % of FTP become Zwift power, so Zwift applies the FTP set in your Zwift profile. Other targets (heart rate, cadence, an effort cue) become free-ride blocks with an on-screen message.

{{% steps %}}

### Download it

On the approved workout's page, click **Download for Zwift**. The first download marks the workout `delivered`.

### Copy it into Zwift's folder

Copy the `.zwo` file into `Documents/Zwift/Workouts/<your Zwift ID>/` on your Mac (`Documents\Zwift\Workouts\<your Zwift ID>\` on Windows). If the numbered folder is missing, start Zwift on that computer and sign in once.

### Restart Zwift

The workout is under **Custom Workouts** in the workout list.

{{% /steps %}}

Zwift on Apple TV or iPad cannot import files: import on a Mac or PC first, and Zwift's cloud sync brings it to your other devices.

### When Zwift cannot take it

A cycling workout with distance, open or heart-rate-bounded steps, or with power targets in watts, cannot be a Zwift workout (`.zwo` power is a fraction of FTP). The page says which step, for example `steps[1].target: watt targets have no Zwift equivalent (Zwift takes % of FTP)`, and the agent gets the same fact as a warning when it proposes the workout.
