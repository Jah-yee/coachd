---
title: Introduction
linkTitle: Documentation
next: /docs/how-it-works
---

coachd is a self-hosted gateway between your AI coach and your Garmin watch. It serves your Garmin data to any AI agent over MCP, and puts the workout you agreed on onto your watch, but only after you approve it with a passkey. One instance serves one runner.

{{< callout type="info" >}}
**Early, but works end to end.** Built by one runner for their own training. While the version is 0.x, a minor release may change configuration or behaviour: read the [changelog](https://github.com/stefiix92/coachd/blob/main/CHANGELOG.md) before upgrading.
{{< /callout >}}

## Why

- Talking training through with an AI (reviews, how you feel, niggles) beats a black-box plan like Garmin's Daily Suggested Workouts.
- A plain AI chat can't see your watch data, forgets context between chats, and can't put a workout on your watch.
- An AI must never push anything to your watch on its own.

coachd is a **gateway, not a coach**. You and your agent decide the training; coachd provides the data, keeps the context, warns, and delivers only what you approved. Its numbers are never verdicts and its warnings never block. There is no chat UI and no built-in LLM: bring Claude, ChatGPT or any MCP client.

## What you need

- **Your own Garmin Connect Personal API key.** It is personal: use it only for your own data, and never host coachd for someone else with it (Garmin's Supplemental Terms §1).
- **A Docker host** with an encrypted disk for coachd's data.
- **A hostname you control** for the public lane (claude.ai, ChatGPT, the approval page on your phone), behind a Cloudflare Tunnel or a reverse proxy. coachd authenticates it with [Cloudflare Access](/docs/install/cloudflare-access/) or its own [built-in auth](/docs/install/builtin-auth/).
- **Optional:** [Tailscale](/docs/install/tailscale/) for agents on your own devices, a fēnix or Forerunner for the [watch app](/docs/watch/), and [ntfy](/docs/install/notifications/) for phone notifications.

{{< cards >}}
  {{< card link="how-it-works" title="How it works" icon="light-bulb" subtitle="Data in, workouts out, and the approval in between." >}}
  {{< card link="install" title="Install" icon="server" subtitle="From an empty host to a working instance." >}}
  {{< card link="agents" title="Connect an agent" icon="chat-alt-2" subtitle="Claude, ChatGPT and local clients." >}}
  {{< card link="watch" title="Watch app" icon="clock" subtitle="Approved workouts on your fēnix or Forerunner." >}}
{{< /cards >}}

Not affiliated with or endorsed by Garmin. Not medical advice.
