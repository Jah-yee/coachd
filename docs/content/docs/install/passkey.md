---
title: Enrol a passkey
weight: 7
---

Your passkey (Face ID, a fingerprint or a security key) approves workouts and, in built-in mode, signs you in. Enrol the first one once the public lane works, from `deploy/` on the host:

```bash
docker compose exec coachd /coachd enroll
```

It prints a link valid for 10 minutes.

{{< tabs >}}
{{< tab name="Access mode" >}}
First sign in at `<COACHD_PUBLIC_URL>/` through Access, in the browser you will enrol from. Then open the link in that signed-in browser.

The token is in the URL fragment, so it never reaches Cloudflare or server logs. For the same reason Access's login redirect drops it: a link opened while signed out lands on the enrol page without its token, and the page asks you to open the enrolment link again.
{{< /tab >}}
{{< tab name="Built-in mode" >}}
Just open the link in the browser you will use. There is no Access login first; the enrolment signs you in. Later you sign in at `<COACHD_PUBLIC_URL>/login` with the passkey.
{{< /tab >}}
{{< /tabs >}}

Passkeys are bound to `COACHD_PUBLIC_URL`. You can enrol more passkeys and revoke them on the page.
