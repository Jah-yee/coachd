# Changelog

Notable changes to coachd. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/). While the version is 0.x, a minor release may change configuration or behaviour; read its notes before upgrading.

The release workflow publishes the section whose heading matches the pushed tag as the release notes.

## [Unreleased]

### Changed

- **Runner page redesign:** a new look for every page, still without JavaScript frameworks or a build step.
  - The coachd logo, a vector drawing, in the header and on the sign-in page, and its navy and green colours throughout; on the dark theme the logo's navy turns light. The browser tab shows the logo on a white tile (SVG, with a PNG for browsers without SVG icons); the home-screen icon is the logo on white.
  - Navigation tabs with icons that fit a phone screen and mark the current page; Connected apps is a tab in builtin mode.
  - Proposals appear as cards with a date badge, a warning count or a coloured status.
  - The home page shows a countdown to the goal.
  - Each workout step has a colour for its kind (warm-up, active, recovery, rest, cool-down) and guides for repeats.
  - On a phone the Approve and Decline buttons stay in view while the workout scrolls.
  - Agent-written text keeps its label, now with its own colour and a ✦ mark.
  - Notes fold their edit form away.
  - Sign-in, enrolment, consent and errors are centred cards.
  - Light and dark themes use `light-dark()`, and page changes cross-fade in browsers that support view transitions.

## [0.1.0] - 2026-10-24

First public release.

### Added

- **Data in:**
  - Garmin Connect Personal API ingest and backfill: activities with their original FIT files, sleep, HRV, resting HR, Body Battery and stress.
  - Deterministic summaries: numbers only, never verdicts.
- **MCP server** (spec 2026-07-28 and 2025-11-25):
  - read tools;
  - `request_sync`;
  - goal and notes;
  - `propose_workout` with sanity checks and warnings, and `cancel_proposal`.
- **Approval and delivery:**
  - The approval page with passkey (WebAuthn) approval.
  - Minimal ntfy notifications.
  - FIT download.
  - Connect IQ watch app with device pairing and delivery confirmation.
- **Network lanes:**
  - The private lane over Tailscale, with scoped static tokens.
  - The public lane in Access mode (Cloudflare Access with Managed OAuth).
  - The public lane in built-in mode: passkey sessions and coachd's own OAuth 2.1 server, with CIMD, opt-in DCR, PKCE, rotating refresh tokens and reuse detection.
- **Operations:**
  - backups and restore;
  - OpenTelemetry with an allowlist-only redaction policy;
  - a local Grafana profile, dashboards and alerts;
  - a hardened compose stack.
- **Releases:** signed multi-arch images on ghcr.io, with an SBOM and build provenance.
