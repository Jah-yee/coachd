---
title: Get the image
weight: 1
---

{{< tabs >}}

{{< tab name="Release image (recommended)" >}}
Releases publish a multi-arch image to `ghcr.io/stefiix92/coachd`, signed with Sigstore keyless signing, with an SBOM and build provenance. Take the digest from the [release notes](https://github.com/stefiix92/coachd/releases) and verify it with [cosign](https://docs.sigstore.dev/cosign/system_config/installation/):

```bash
cosign verify ghcr.io/stefiix92/coachd@sha256:<digest> \
  --certificate-identity-regexp '^https://github.com/stefiix92/coachd/.github/workflows/release.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Pin it by digest in `.env`, so a re-pushed tag can never change what you run, and pull it:

```bash {filename=".env"}
COACHD_IMAGE=ghcr.io/stefiix92/coachd:<version>@sha256:<digest>
```

```bash
docker compose pull coachd
```

Start it with `docker compose up -d --no-build coachd` once the secrets exist.
{{< /tab >}}

{{< tab name="Build it yourself" >}}
```bash
docker compose build        # builds coachd:local
```

With `COACHD_IMAGE` empty, compose runs `coachd:local`.
{{< /tab >}}

{{< /tabs >}}
