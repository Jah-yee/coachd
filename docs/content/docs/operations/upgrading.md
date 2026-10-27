---
title: Upgrading
weight: 4
---

Read the [changelog](https://github.com/stefiix92/coachd/blob/main/CHANGELOG.md) first: while the version is 0.x, a minor release may change configuration or behaviour.

**Release image:** verify the new digest as in [Get the image](/docs/install/image/), set it in `COACHD_IMAGE`, then:

```bash
docker compose pull coachd && docker compose up -d --no-build coachd
```

**Your own build:** `git pull`, then `docker compose build && docker compose up -d coachd`.

The other images in the compose files are pinned by digest. To move one, update its tag and digest (`docker buildx imagetools inspect <image>:<tag>` resolves it).
