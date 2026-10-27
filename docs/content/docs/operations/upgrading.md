---
title: Upgrading
weight: 4
---

While the version is 0.x, a minor release may change configuration or behaviour. Read the [changelog](https://github.com/stefiix92/coachd/blob/main/CHANGELOG.md) before upgrading.

## coachd

With the release image, verify the new digest as in [Get the image](/docs/install/image/), set it in `.env`, then:

```bash
docker compose pull coachd && docker compose up -d --no-build coachd
```

With your own build, pull the new source and run `docker compose build && docker compose up -d coachd`.

## The other images

Every other image in the compose files is pinned by multi-arch index digest. To upgrade one, resolve a new digest with `docker buildx imagetools inspect <image>:<tag>` and update both the tag and the digest.

## Upgrade notes

### From M2a: `ntfy_token` must exist

Compose mounts the `ntfy_token` secret, so `docker compose up -d` fails ("bind source path does not exist") until `secrets/ntfy_token` exists, even with ntfy unused. Before the first `up` of the new version, create it empty and hand it to coachd as in [ntfy](/docs/install/notifications/#ntfy):

```bash
umask 077; : > secrets/ntfy_token
```

then the `docker run ... chown 65532:65532 ... chmod 0400` step.

### From the shared `egress` network

Each service with internet access now has its own egress network. After `docker compose up -d`, remove the leftover shared one:

```bash
docker network rm coachd_egress
```
