---
title: Security model
weight: 3
---

coachd handles health data and controls what reaches a runner's watch. To report a vulnerability, follow the [security policy](https://github.com/stefiix92/coachd/blob/main/SECURITY.md): privately, never in a public issue.

## Invariants

- **No agent path to the watch.** No MCP tool or agent credential can approve, deliver, withdraw or alter an approved workout. Approval needs your WebAuthn passkey on coachd's page. Approval nonces are single-use and HMAC-signed, bound to the proposal and a hash of its content; approving a week binds every proposal in the set, all or nothing. coachd enforces this itself, never relying on client-side confirmation.
- **The Garmin key stays server-side.** It never reaches agents, logs, telemetry or the page.
- **Untrusted text stays labelled.** Garmin activity names and descriptions, agent rationale and OAuth client names are shown as untrusted.
- **One runner per instance.** No multi-user or hosted features.

## Access

- **Public lane:** either [Cloudflare Access](/docs/install/cloudflare-access/) (coachd validates the Access JWT on every request) or [built-in auth](/docs/install/builtin-auth/) (passkey sessions for the page, coachd's own OAuth 2.1 server with PKCE, rotating refresh tokens and reuse detection for agents). In both, every write on the page needs your passkey. In built-in mode only login, enrolment, OAuth metadata, authorize, token, revoke, register and `/device/v1` are reachable without a session or token, and each checks its own credential. Agent tokens work only on `/mcp`.
- **Private lane:** [Tailscale](/docs/install/tailscale/) (no Funnel) with hashed, scoped static tokens.
- **Watch:** a device token per paired watch, revocable at once.
- **No open ports to the network.** The only published port is coachd's public listener on loopback (`127.0.0.1:8080`), for a tunnel or reverse proxy on the host. The private listener (8081) and the ops listener (9090, unauthenticated pprof) are never published.

## Data

- Secrets live in Docker secret files, each owned by the one container that reads it.
- Data sits on an [encrypted disk](/docs/install/#put-the-data-on-an-encrypted-disk).
- `nonce.key` (the HMAC key for approval and login ceremonies) lives in `/data` and is created on first start when the public lane is enabled. It is not in backups; losing it only invalidates ceremonies in flight. In built-in mode it also signs consent requests and DCR client ids: losing it means DCR apps cannot start new authorizations (their client id no longer verifies) and must register again; existing DCR connections keep refreshing only while `COACHD_OAUTH_DCR=on`.
- An **audit log** records every tool call, approval and authentication failure.

## Telemetry

Logs, metrics, traces and profiles carry IDs, timings and outcomes only: never health data, chat or note content, or tokens. The redaction is an allowlist of declared keys, enforced by tests.

## Containers

- Release images are multi-arch, signed with Sigstore (keyless, from the release workflow) and carry an SBOM and build provenance. You can also [build the image yourself](/docs/install/image/).
- The coachd container is distroless, runs as non-root, and has a read-only filesystem.
- Every service is `cap_drop: [ALL]`, `no-new-privileges`, read-only rootfs. Exceptions: coachd has a 16 MB `noexec` tmpfs at `/tmp` for SQLite temp files; the tailscale key file is owned 0:0 so no capability is added; VictoriaMetrics and VictoriaLogs run as root in their images (they own their named volumes); the local Grafana has a 16 MB tmpfs at `/tmp` for its assembled provisioning.
- Every container uses the `local` log driver, rotated at 10 MB × 3 files. VictoriaLogs keeps 30 days of logs, capped at 5 GiB on disk (older daily partitions are dropped first), so a log flood cannot fill the SD card.
- Memory limits: coachd 128m (with `GOMEMLIMIT=100MiB`), tailscale 96m, alloy 256m, VictoriaMetrics 192m, VictoriaLogs 192m, pdc-agent 64m, local grafana 256m.
- Images are pinned by multi-arch index digest.

## Networks

- `internal` (internal) joins coachd, tailscale, Alloy, VictoriaMetrics and VictoriaLogs. Alloy is on `internal` only unless Grafana Cloud is on.
- `pdc` (internal, Grafana Cloud only) joins pdc-agent, VictoriaMetrics and VictoriaLogs.
- With local monitoring on, `grafana-data` (internal) joins Grafana, VictoriaMetrics and VictoriaLogs, and `grafana-ui` (internal) joins Grafana and tailscale.
- Each service with internet access has its own egress network, so no two services share one and an egress network is never a path between containers: coachd `coachd-egress` (also carries the `127.0.0.1:8080` publish), tailscale `ts-egress`, with Grafana Cloud on Alloy `alloy-egress` and pdc-agent `pdc-egress`, and with local monitoring on Grafana `grafana-egress`.

## Known risks

- **Garmin Personal API:** launched 2026-10-21; rate limits are undocumented and may change. The key is personal and for your own use only (Supplemental Terms §1); hosting coachd for other people would need Garmin's Developer Program.
- **No official self-serve way to push workouts to the watch.** Hence the Connect IQ app, with FIT files as the fallback.
- **The spike warning** rests on a single cohort study (Frandsen 2025). It warns and never blocks.
- **The PDC agent's token** is visible in its process arguments on the host: see [Grafana Cloud](/docs/operations/grafana-cloud/#the-pdc-agent).
