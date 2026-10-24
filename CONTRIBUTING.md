# Contributing

Thanks for helping. coachd is small and opinionated, so please open an issue to discuss a feature before writing it. Bug fixes and test improvements are welcome straight away.

## Ground rules

These come from the design ([`docs/design/coachd-design.md`](docs/design/coachd-design.md)) and are not up for negotiation in a pull request:

- **A gateway, not a coach.** No coaching logic, readiness verdicts or blocking training rules. Warnings are fine; blocks are not. Summaries are numbers only.
- **No agent path to the watch.** No MCP tool or agent credential may approve, deliver, withdraw or alter an approved workout. Approval needs the runner's passkey.
- **The Garmin key stays server-side.** It never reaches agents, logs, telemetry or the page.
- **Telemetry is an allowlist.** Logs, span attributes and metric labels carry only declared keys (IDs, dates, outcomes, durations, counts, error classes). Never health values, note text, tokens or names. Tests enforce this.
- **Untrusted text stays labelled.** Garmin activity names and descriptions, agent rationale and OAuth client names are untrusted.
- **One runner per instance.** No multi-user or hosted features.

## Development

You need Go (the version in `go.mod`), and Docker for the image and compose checks.

```bash
go test -race ./...                            # all tests
go vet ./...
gofmt -l .                                     # must print nothing
CGO_ENABLED=0 go build ./cmd/coachd            # static binary; keep the build cgo-free
docker compose -f deploy/compose.yaml config   # compose check
```

The watch app lives in `watch/`; see [`watch/README.md`](watch/README.md).

## Pull requests

- Keep each PR to one change, with tests: a failing test first for bug fixes.
- Match the surrounding code: its naming, comment density and error handling.
- New dependencies need a reason: a small dependency footprint is a review criterion.
- Update `deploy/README.md` when you change configuration or operations.
- CI (vet, race tests, govulncheck, the multi-arch image build) must pass.

## Sign-off (DCO)

Every commit needs a `Signed-off-by:` line certifying the [Developer Certificate of Origin](https://developercertificate.org/), which says you have the right to submit the change under the project's license (AGPL-3.0-only):

```bash
git commit -s
```

## Code of conduct

Everyone taking part follows the [Code of Conduct](CODE_OF_CONDUCT.md).

## Releasing (maintainers)

1. Move the `## [Unreleased]` entries in `CHANGELOG.md` into a new `## [X.Y.Z] - YYYY-MM-DD` section, and commit it.
2. Tag and push it: `git tag -a vX.Y.Z -m vX.Y.Z && git push origin vX.Y.Z`.
3. The `release` workflow then:
   - runs the tests;
   - builds and pushes the multi-arch image to `ghcr.io/<owner>/coachd`, with an SBOM and provenance;
   - signs it with Sigstore keyless signing;
   - creates the GitHub release from that changelog section, with the image digest and the `cosign verify` command.

   It fails if the changelog has no section for the tag.
4. On the first release, check that the ghcr.io package is public (Package settings → Change visibility).

## Reporting bugs

Open an issue with the version or commit, which mode you run (Access or built-in auth), what you did and what happened. Never paste tokens, passkeys, Garmin keys, `.env` contents or health data; redact them. Report security problems privately: see [`SECURITY.md`](SECURITY.md).
