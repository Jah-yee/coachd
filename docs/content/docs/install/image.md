---
title: Get the image
weight: 1
---

{{< tabs >}}

{{< tab name="Release image (recommended)" >}}
Each tagged release publishes a multi-arch image (arm64 and amd64) to `ghcr.io/stefiix92/coachd`. The release workflow signs it with Sigstore keyless signing, and it carries an SBOM and build provenance.

Take the digest from the [release notes](https://github.com/stefiix92/coachd/releases) and verify it before you run it, with [cosign](https://docs.sigstore.dev/cosign/system_config/installation/):

```bash
cosign verify ghcr.io/stefiix92/coachd@sha256:<digest> \
  --certificate-identity-regexp '^https://github.com/stefiix92/coachd/.github/workflows/release.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Then set the image in `.env`:

```bash {filename=".env"}
COACHD_IMAGE=ghcr.io/stefiix92/coachd:<version>@sha256:<digest>
```

and pull it:

```bash
docker compose pull coachd
```

Once the secrets exist, start it with `docker compose up -d --no-build coachd`; upgrades use the same two commands. Pinning the digest means a re-pushed tag can never change what you run.
{{< /tab >}}

{{< tab name="Build it yourself" >}}
On the host, from a checkout:

```bash
docker compose build        # builds coachd:local
```

Or build elsewhere and load it onto the host:

```bash
docker buildx build --platform linux/arm64 --load -t coachd:local ..
docker save coachd:local | ssh pi docker load
```

With `COACHD_IMAGE` empty, compose runs `coachd:local`.
{{< /tab >}}

{{< /tabs >}}

{{< callout type="info" >}}
Don't start the whole stack yet: compose mounts every secret file, so `docker compose up` fails until the [secrets](../secrets/) exist.
{{< /callout >}}
