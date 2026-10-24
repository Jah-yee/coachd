# Changelog

Notable changes to coachd. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/). While the version is 0.x, a minor release may change configuration or behaviour; read its notes before upgrading.

The release workflow publishes the section whose heading matches the pushed tag as the release notes.

## [Unreleased]

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
