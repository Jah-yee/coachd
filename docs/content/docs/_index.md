---
title: Introduction
linkTitle: Documentation
next: /docs/how-it-works
---

coachd is a self-hosted gateway between your AI coach and your Garmin watch. It serves your Garmin data to any AI agent and puts the workout you agreed on onto your watch, but only after you approve it.

It ships as one Docker image that runs anywhere: a Raspberry Pi, a home server or a VPS. One instance serves one runner. Your Garmin key stays on your hardware.

{{< callout type="info" >}}
**Status:** early and built by one runner for their own training, but it works end to end: Garmin data in, MCP out, passkey-approved workouts to the watch. Expect rough edges. While the version is 0.x, a minor release may change configuration or behaviour, so read the [changelog](https://github.com/stefiix92/coachd/blob/main/CHANGELOG.md) before upgrading.
{{< /callout >}}

## Why

- Talking training through with an AI (reviews, how you feel, weather, niggles) works better than a black-box plan like Garmin's Daily Suggested Workouts.
- A plain AI chat can't see your watch data, forgets context between chats, and can't put a workout on your watch.
- An AI must never push anything to your watch on its own.

## A gateway, not a coach

- The AI agent and you decide the training.
- coachd provides the data, keeps the context, warns, and delivers only after your explicit approval.

There is no chat UI and no built-in LLM. You talk to Claude (or ChatGPT, Gemini, Cursor, Open WebUI or a local model), and the agent calls coachd over **MCP**. coachd's summaries are numbers, never verdicts, and its warnings never block.

## What it does

1. **Data in.** It pulls your data from the official [Garmin Connect Personal API](https://developer.garmin.com/gc-developer-program/personal/) and computes simple totals so the agent doesn't do arithmetic.
2. **Context.** It stores the goal the agent sets and a few durable notes (injuries, availability, preferences), so any agent or new chat starts informed.
3. **Workout out.** The agent proposes a structured workout, you approve it with a passkey, and coachd delivers it to the watch app, as a FIT file, or as a Zwift workout.
4. **Your own rating.** The "How did you feel?" and RPE answers you give on the watch after a workout reach the agent too.
5. **Planned vs actual.** It reads the executed steps back from each activity and links them to the workout it delivered.

[How it works](/docs/how-it-works/) explains each of these.

## What you need

- **Your own Garmin Connect Personal API key.** It is personal: use it only for your own data, never share it, and never host coachd for someone else with it (Garmin's Supplemental Terms §1). Each runner self-hosts their own instance.
- **A Docker host:** a Raspberry Pi 4/5, a home server or a small VPS (arm64 or amd64), with an encrypted disk for coachd's data.
- **A way in for your agents:**
  - **Public lane** (claude.ai, ChatGPT, the approval page on your phone): a hostname you control, exposed through a Cloudflare Tunnel or a reverse proxy such as Caddy, and authenticated by [Cloudflare Access](/docs/install/cloudflare-access/) or by coachd's own [built-in auth](/docs/install/builtin-auth/).
  - **Private lane:** [Tailscale](/docs/install/tailscale/), for agents on your own devices (Claude Code, Cursor, local models), with scoped static tokens.
- **Optional:** a fēnix or Forerunner watch for the [watch app](/docs/watch/), an [ntfy](/docs/install/notifications/) topic for phone notifications, and a [post-run webhook](/docs/install/notifications/#post-run-webhook) for your own automation.

## Next

{{< cards >}}
  {{< card link="how-it-works" title="How it works" icon="light-bulb" subtitle="Data in, workouts out, and the approval in between." >}}
  {{< card link="install" title="Install" icon="server" subtitle="From an empty host to a working instance." >}}
  {{< card link="agents" title="Connect an agent" icon="chat-alt-2" subtitle="Claude, ChatGPT and local clients." >}}
  {{< card link="watch" title="Watch app" icon="clock" subtitle="Approved workouts on your fēnix or Forerunner." >}}
{{< /cards >}}

## Disclaimer

Not affiliated with or endorsed by Garmin. Not medical advice. If something hurts, stop and see a professional.

coachd is licensed under the [GNU Affero General Public License v3.0](https://github.com/stefiix92/coachd/blob/main/LICENSE) (AGPL-3.0-only). You may use, modify and self-host it freely. If you run a modified version as a service for others, you must publish your changes under the same license.
