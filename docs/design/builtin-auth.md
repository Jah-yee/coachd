# Built-in auth for the public lane: design

Amends `coachd-design.md` §8 (Approval), §9 and §10. Markers as there: **[owner]** owner decision, **[call]** designer's call, **[builder]** left to the implementer. A security review of the first draft (1 critical, 10 important and 18 minor findings) is folded in; its finding ids (C1, I1–I10, M1–M18) are cited where they shaped a rule.

## 1. Intent

Today the public lane works only behind Cloudflare Access. Access does the runner's login and MFA, and its Managed OAuth is the authorization server for remote AI clients (claude.ai, ChatGPT). That is fine for the owner, but the open-source release must not require a Cloudflare account, and a hosted coachd later needs its own auth anyway.

**[owner, 2026-10-24]** coachd gains a **built-in auth mode**:
- The runner signs in to the page with a passkey (coachd already has passkeys).
- coachd is its own OAuth 2.1 authorization server for MCP clients. A client gets a token only after the runner, signed in, approves it with a passkey on a consent page.

Cloudflare Access stays as the other mode, unchanged. The owner's Pi keeps running Access mode until the owner decides otherwise.

## 2. Goals, non-goals, anti-goals

**Goals:**
- claude.ai (web, desktop, mobile), Claude Code and ChatGPT connect to a builtin-mode coachd with no Cloudflare account. All three identify themselves with Client ID Metadata Documents (CIMD), which were verified live on 2026-10-24.
- Any other client that follows the MCP authorization spec (2025-11-25 or 2026-07-28) connects too, including through Dynamic Client Registration (DCR) when the runner turns it on.
- Agent tokens and the runner's passkey session are separate credentials. An agent token works only on `/mcp`; every page needs a passkey session.
- The runner sees every connected app on the page and can revoke it, effective at once.
- A stranger on the internet can neither lock the runner out nor fill the Pi's disk.

**Non-goals:**
- Multi-user or hosted operation. There is still one runner per instance (main spec §14, Garmin terms).
- OpenID Connect: no ID tokens, no userinfo, no `openid-configuration`.
- Confidential clients: no client secrets, no `private_key_jwt` verification.
- Token introspection, the device-code grant, `client_credentials`.
- CORS on `/mcp` (browser-based MCP clients).
- Per-IP rate limits, and therefore trusting proxy headers.
- Tailscale Funnel as a documented exposure path.

**Anti-goals** (failures even if it "works"):
- Any path where an agent credential approves, delivers or withdraws a workout. The invariant is unchanged.
- An open redirect: from the OAuth endpoints to an unregistered URI, or from `next` after login.
- coachd fetching a URL for anyone who is not the signed-in runner (SSRF, origin-IP leak).
- Tokens, codes, verifiers, client names, redirect URIs or CIMD URLs in logs, spans, metric labels or audit targets.
- Built-in mode turning on by accident. Setups that set only `COACHD_PUBLIC_URL` today, such as watch-only ones, must not suddenly expose the page and `/mcp`.

## 3. Modes and configuration

New setting `COACHD_PUBLIC_AUTH` **[call]**:

| Value | Requires | Public listener serves |
|---|---|---|
| unset | — | Today's behaviour: Access mode when all three `COACHD_ACCESS_*` are set, otherwise only `/device/v1` when `COACHD_DEVICE_API=on`, otherwise disabled |
| `access` | all three `COACHD_ACCESS_*` and `COACHD_PUBLIC_URL` | as today |
| `builtin` | `COACHD_PUBLIC_URL`; every `COACHD_ACCESS_*` empty | page, `/mcp`, the OAuth endpoints, the well-known metadata, and `/device/v1` when enabled |

- **Other combinations:** any other value, or a combination outside the table, is a startup error.
- **The public URL in builtin mode** must be `https://`, except `http://localhost[:port]`, which is for development only and logs a startup warning (M1). `http://127.0.0.1` is refused in builtin mode, because WebAuthn does not accept an IP address as the relying party.
- **`COACHD_OAUTH_DCR`:** `on` or `off`, default `off`; any other value is an error. It is allowed only in builtin mode (I4).
- **ntfy:** `COACHD_NTFY_URL` requires a public lane with the page, `access` or `builtin`.
- **Private lane:** unchanged in both modes (Tailscale, static `cdt_` tokens). The public `/mcp` never accepts a `cdt_` token (M9).
- **Network exposure:** unchanged; the only published port is `127.0.0.1:8080`. In builtin mode the runner puts a TLS-terminating proxy in front (§10).
- **URLs:** every URL coachd emits (issuer, endpoints, redirects, `resource_metadata`) is built from `COACHD_PUBLIC_URL`, never from `Host` or `X-Forwarded-*`.
- **OAuth state outside builtin mode:** while not in builtin mode, coachd deletes all OAuth grants, codes and tokens at startup (M13). Switching to Access and back therefore never revives an old grant.

## 4. The page in builtin mode

### 4.1 Wiring (I6)

Builtin mode replaces `web.Server.publicOnly` with a **default-deny `sessionGate`**:
- **Exempt routes.** An explicit allowlist passes without a session: `GET /login`, `POST /login/begin`, `POST /login/finish`, `GET /enroll`, `POST /enroll/begin`, `POST /enroll/finish`, the static assets, and `GET /oauth/authorize` (which then redirects, §5.3).
- **Every other route needs a valid passkey session.**
  - A GET or HEAD without one redirects to `/login?next=<path and query>`.
  - Any other method answers 401.
- **The public-lane identity** is set only after a session is found, so existing handlers keep working unchanged.
- **No `auth.Middleware`.** The gate never goes through it, so a crawler hitting the page writes no audit row and gets no `WWW-Authenticate: Bearer`.
- **A test walks every route** registered in `routes()` without a cookie. Each must be exempt, or redirect or 401.

**Mounting:**
- **Inside `web.Handler()`:** the consent routes (`/oauth/authorize`, `/oauth/authorize/begin`, `/finish`, `/deny`). They keep the page's CSP, `frame-ancestors 'none'`, `CrossOriginProtection` and the body cap.
- **In `publicMux`, outside the page handler and `CrossOriginProtection`:** the metadata, token, register and revoke endpoints, with CORS.
- **JSON 404 before the gate (M7):** the whole `/.well-known/` prefix, apart from the two documents in §5.1, plus the legacy `/authorize`, `/token` and `/register`. Discovery probes must never get the HTML login.

### 4.2 Login, `next`, enrolment

- **`next` (I2).**
  - Parse it with `url.Parse` and reject:
    - any byte ≤ 0x20, and any `\`;
    - a non-empty `Scheme`, `Host`, `Opaque` or `User`;
    - a path that does not start with exactly one `/`;
    - a total length over 8 KiB. That is large enough for an authorize URL that carries a DCR client_id (about 2.5 KiB) and a 1 KiB `state`.
  - Re-serialise it from `EscapedPath()` plus `RawQuery`. A rejected `next` becomes `/`.
  - The client navigates only to the server-validated value. Fuzz tests cover `/\evil`, `/%09/evil`, `//evil`, `/\t/evil` and `https:evil`.
- **Rate limits that cannot lock the runner out (C1).**
  - `begin` endpoints are not limited; they are stateless HMAC mints.
  - `finish` endpoints verify the assertion first, and a verified assertion always proceeds.
  - Only failures draw from a global failure bucket of 30 per minute, burst 10. With the bucket empty, a failure is answered 429, counted, and not audited.
  - Login, enrolment and consent each have their own failure bucket.
  - A test saturates the bucket with bad assertions and shows that a valid login still succeeds.
- **First passkey:** `coachd enroll` from the host shell, as today.
- **Passkey credential ids (M15).** `/login/begin` still lists the enrolled credential ids, now to anyone. That reveals how many passkeys exist and their random ids, nothing more. This is accepted **[call]**, because enrolment uses `ResidentKeyRequirementPreferred` and discoverable login could break passkeys that are not resident.
- **Reads need a session, writes are unchanged.** Approve still needs a fresh assertion; decline, withdraw, FIT, notes and passkey changes still need the session (R59 stays, now as defence in depth). The ntfy link `/p/<id>` costs one sign-in when there is no session.

### 4.3 Connected apps page

`/apps`, linked from home, has one row per grant with a live token (M13):
- the app's web address, as punycode (§5.2), shown first;
- the name, labelled "name given by the app";
- the redirect host;
- the scopes;
- created and last-used times;
- a Revoke button (session POST).

Grants that never got a token, or whose tokens all expired, are hidden. In Access mode the page is not served.

## 5. Authorization server

Issuer: `COACHD_PUBLIC_URL` exactly as `config.ParsePublicURL` normalises it: lower-case host, no trailing slash.

### 5.1 Endpoints and metadata

| Path | Purpose |
|---|---|
| `GET /.well-known/oauth-protected-resource/mcp` and `GET /.well-known/oauth-protected-resource` | the **same** RFC 9728 document at both paths (M8): `resource` `<PUBLIC_URL>/mcp`, `authorization_servers` `[<PUBLIC_URL>]`, `scopes_supported` `["coach:read","coach:write"]`, `bearer_methods_supported` `["header"]` |
| `GET /.well-known/oauth-authorization-server` | RFC 8414 metadata (below) |
| `GET /oauth/authorize` | authorization request (§5.3) |
| `POST /oauth/authorize/begin`, `/finish`, `/deny` | consent ceremony (§5.3) |
| `POST /oauth/token` | code and refresh grants (§5.4) |
| `POST /oauth/register` | DCR, only when `COACHD_OAUTH_DCR=on` (§5.2) |
| `POST /oauth/revoke` | RFC 7009 revocation (§5.5) |

**AS metadata:**
- `issuer`, `authorization_endpoint`, `token_endpoint`, `revocation_endpoint`, and `registration_endpoint` only when DCR is on.
- `response_types_supported` `["code"]`, `grant_types_supported` `["authorization_code","refresh_token"]`, `code_challenge_methods_supported` `["S256"]`.
- `token_endpoint_auth_methods_supported` `["none"]`, `revocation_endpoint_auth_methods_supported` `["none"]`.
- `scopes_supported` `["coach:read","coach:write","offline_access"]`.
- `client_id_metadata_document_supported` `true`, `authorization_response_iss_parameter_supported` `true`.

Claude picks CIMD only when both `client_id_metadata_document_supported` and `none` are advertised. ChatGPT uses the intersection of methods, which here resolves to `none`.

**Headers:**
- The metadata, register, token and revoke endpoints answer CORS `Access-Control-Allow-Origin: *`, with no credentials and an `OPTIONS` preflight.
- The token and register responses carry `Cache-Control: no-store`. The metadata may be cached for 1 h.

### 5.2 Client identification

**CIMD (primary).** A `client_id` that is an `https` URL is a CIMD client. Every CIMD client is a public client.

**The URL.** It must:
- have a path other than `/`;
- have no query, fragment, userinfo or dot segments;
- use the default port;
- be at most 512 bytes;
- have a host that passes `idna.Lookup.ToASCII`.

The host is kept and shown in ASCII (punycode) form (I5).

**When it is fetched (I3).** Only from `GET /oauth/authorize` with a valid runner session. The token, refresh and revoke endpoints use the stored row and never fetch (M5).

**The fetch.**
- https only and no redirects; 5 s total timeout; body capped at 16 KiB; must parse as JSON. `HTTP_PROXY` is ignored.
- It uses a plain `http.Client` with no OpenTelemetry instrumentation, and failures log `error_class` only (M10).
- **Addresses (M11):** the dialer resolves the host and refuses the fetch if **any** resolved address is not public unicast. It then connects to a checked address, so DNS rebinding cannot swap it.
  - The `netip` predicates refuse loopback, private, link-local, multicast and unspecified addresses.
  - Explicit prefixes refuse: `0.0.0.0/8`, `100.64.0.0/10` (tailnet addresses), `192.0.0.0/24`, `192.0.2.0/24`, `198.18.0.0/15`, `198.51.100.0/24`, `203.0.113.0/24`, `240.0.0.0/4`, `64:ff9b::/96`, `64:ff9b:1::/48`, `100::/64`, `2001:db8::/32`, `2002::/16`, `fc00::/7`, `fec0::/10`, and IPv4-mapped forms.
  - A hairpin to the deployment's own WAN address passes this check, which is acceptable: it reaches only what is already public.
- **Errors:** every fetch failure shows the same error page, so the fetch cannot be used as a port oracle.

**Validating the document (I8, M6).**
- **Refuse:**
  - a `client_id` that differs from the URL byte for byte;
  - any `client_secret` or `client_secret_expires_at`;
  - a `client_secret_*` value in `token_endpoint_auth_method`.
- **Ignore:**
  - any other `token_endpoint_auth_method`, such as ChatGPT's `private_key_jwt`;
  - unknown fields.
- **Must include if present:** `grant_types` must include `authorization_code`; `response_types` must include `code`. Extra entries, such as claude.ai's jwt-bearer grant, are fine.
- **`redirect_uris`:** invalid entries are dropped. The document is refused only when no valid entry is left or there are more than 20 entries.
- **`client_name`:**
  - control characters and bidi controls are stripped;
  - the name is truncated to 100 characters for display;
  - if missing, the page shows "unnamed".
- **Test fixtures:** the live documents of claude.ai, Claude Code and ChatGPT, fetched 2026-10-24.

**Caching (M5).**
- A good document is stored for `max-age` clamped to [5 min, 24 h], default 1 h.
- A failed fetch is remembered **in memory only** for 1 min, and never when the cause is coachd's own fetch budget (30 per minute globally).
- A client that has a live grant and whose refetch fails keeps its last good document. Stale-if-error never extends to new redirect URIs.

**DCR (opt-in, I4).** Off by default, because claude.ai, Claude Code and ChatGPT all use CIMD, and open registration is the easiest way to phish a consent.
- When on, registration is **stateless**. The `client_id` is `dcr_` plus base64url of an HMAC-signed blob holding `{redirect_uris, client_name, iat}`, signed with a key derived from `nonce.key` with its own label. Nothing is stored until consent, so there is nothing to fill and no registration bucket.
- **Limits:**
  - at most 3 redirect URIs of at most 512 bytes each;
  - name rules as for CIMD;
  - `grant_types` and `response_types` beyond code and refresh are refused.
- Every registration is `token_endpoint_auth_method: none`, and the response says so (RFC 7591 §3.2.1).
- The DCR client_id does not expire.
- While `COACHD_OAUTH_DCR` is off, grants of DCR clients are refused: at refresh with `invalid_grant`, and on `/mcp` with 401. Nothing is revoked, so turning DCR on again restores them unless the runner revoked them on `/apps`. (Ruling R-BA-F2.)

**Redirect URIs** (both kinds):
- **Allowed forms:**
  - `https://` with a host;
  - `http://127.0.0.1`, `http://[::1]` or `http://localhost` (loopback), with any port.
- **Refused:** no userinfo, no fragment, no other schemes (custom schemes are refused **[call]**), and nothing on coachd's own origin (M12).
- **Matching:** exact string comparison, except that loopback ignores the port (RFC 8252 §7.3).
- **Omitted at authorize:** allowed only when exactly one non-loopback URI is registered (OAuth 2.1 §2.3.2).

### 5.3 Authorization and consent

**Without a session (I3):** `GET /oauth/authorize` checks only the syntax of `client_id` and `redirect_uri` (§5.2 forms). Then it redirects **locally** to `/login?next=<this authorize URL>`. It never fetches, never stores, and never redirects to the client.

**With a session**, in this order:

1. **The client.** It resolves (CIMD fetch or cache, or the signed DCR id) and `redirect_uri` matches. If not, coachd shows an error page and never redirects.
2. **The request.** From here on, failures redirect to the client with `error`, `state` and `iss`.
   - Duplicate parameters are `invalid_request` (M12).
   - `response_type=code`.
   - `code_challenge` must be exactly 43 base64url characters, with `code_challenge_method=S256`. `plain` and a missing challenge are refused.
   - `state` is at most 1 KiB.
   - `scope` must be a subset of the supported scopes. Absent means both `coach:*`; `offline_access` is accepted and ignored.
   - `resource`, if present, equals `<PUBLIC_URL>/mcp` or `<PUBLIC_URL>`, with scheme and host compared case-insensitively and one trailing slash ignored. Anything else is `invalid_target`. Absent means `/mcp`.
3. **No passkey enrolled.** The page says so and never redirects.

**The consent page** renders a **signed request token**, the ceremony state that carries every validated field. It shows:
- **who asks:** the app's web address (punycode, registrable domain emphasised) as the primary identity, never called "verified". The name follows, labelled "name given by the app". A DCR client shows "registered directly with coachd; its identity is not checked" instead of a web address.
- **where the browser goes next:** the redirect host, prominent. There is a warning when its registrable domain (`publicsuffix.EffectiveTLDPlusOne`) differs from the client_id host's (I5).
- **loopback-only redirects (MCP SHOULD):** "Any program on this computer can claim to be this app. Approve only if you just started the connection yourself."
- **the scopes in plain words:**
  - `coach:read`: your Garmin data, summaries, goal and notes;
  - `coach:write`: set your goal and notes, and propose workouts. **Workouts still need your passkey to approve.**
  - `coach:write` is a checkbox, on by default for CIMD clients and **off** for DCR clients.
  - A read-only grant cannot be widened later; to widen it, revoke and reconnect (M16).
- **DCR clients only:** a required checkbox, "I recognise <redirect host>".

**The ceremony (M2, M3).**
- **begin** takes only the signed request token and the checkbox values. It re-validates the client and redirect, then mints the WebAuthn assertion options for a new `passkey.Purpose` `consent`. The ceremony binds the request token plus the chosen scopes, so `finish` never accepts a scopes field.
- **finish** verifies the assertion (user verification required). Then, in **one transaction**, it:
  1. consumes the WebAuthn nonce (`ConsumeNonce`);
  2. records the passkey use (`UsePasskey`, the sign count);
  3. creates the grant with the approving passkey id;
  4. creates the code: 32 random bytes, stored as SHA-256, single use, 5 min.

  It answers JSON `{"redirect": "<redirect_uri with code, state, iss>"}`, appending to any existing query. The page's script navigates there.
- **deny** takes only the signed request token, re-validates, and answers JSON with the `error=access_denied` redirect (with `state` and `iss`). It is audited only because the token verified.
- **Every consent creates a new grant** **[call]**. It counts as active, and appears on Connected apps, only once its code is exchanged (M13).

### 5.4 Token endpoint

Form-encoded POST. It answers JSON with `Cache-Control: no-store`. Errors follow RFC 6749 §5.2, and duplicate parameters are `invalid_request`.

**`authorization_code`** requires `code`, `client_id` and `code_verifier`.
- `redirect_uri` is **optional** (OAuth 2.1 §4.1.3, I9). If present, it must equal the authorization request's exact value, port included.
- `resource` is optional and, if present, must match the grant's.
- The code must be unexpired and unused, and belong to that client. PKCE S256 must verify: `code_verifier` is 43 to 128 unreserved characters, compared with `subtle.ConstantTimeCompare`.
- **Code reuse (M4):** a used code presented again with a matching `client_id`, redirect and PKCE revokes the grant with all its tokens. It writes the audit row `oauth_code_reuse`, notifies (§9), and answers `invalid_grant`. A mismatching replay is just `invalid_grant`.

**`refresh_token`** requires `refresh_token` and `client_id`.
- `scope` is optional and may only narrow. A narrowed set carries to the new refresh token.
- `resource` follows the same rule as for the code.
- **Rotation:** each refresh token is single use; the response carries a new one.
- **Reuse with a 60 s grace (I10, Auth0's reuse-interval model):** say the presented token was already used.
  - **Within 60 s of its use, by the same client:** issue another fresh pair and revoke nothing. Each issued refresh token stays individually single use.
  - **After 60 s:** revoke the whole grant, write the audit row `oauth_refresh_reuse`, notify (§9), and answer `invalid_grant`.

**Tokens:**

| Kind | Format | Lifetime |
|---|---|---|
| access | `cda_` + base64url(32 random bytes) | 1 h **[call]** |
| refresh | `cdr_` + base64url(32 random bytes) | 30 days from issue; each rotation issues a fresh 30 days |

- Only SHA-256 hashes are stored.
- A refresh token is issued on every code exchange, whether or not `offline_access` was asked for.
- The response's `scope` lists the granted `coach:*` scopes.

### 5.5 Revocation

- **`/oauth/revoke`** (RFC 7009):
  - A valid token whose grant belongs to the given `client_id` (when one is sent) revokes the grant.
  - It answers 200 whatever the token's validity: valid, unknown, expired or someone else's.
  - Only a malformed request (no `token` parameter, an unparseable form) gets 400 `invalid_request` (RFC 7009 §2.2). An oversized body gets 413, and coachd's own failure gets 500.
- **Revoke button:** sets `revoked_at` on the grant, and every token of the grant fails on its next request. There is no cache.
- **Revoking a passkey** does not revoke the grants it approved **[call]**. They are listed and revoked separately.

## 6. Resource server: `/mcp` in builtin mode

- **A valid bearer token** in `Authorization` (never in the query): the hash is found, `kind=access`, unexpired, and its grant is not revoked. The identity becomes (I7):
  - `Lane public`;
  - `Subject grant:<id>`, so `auth.Limiter` buckets per grant;
  - `TokenID g<id>`;
  - `Scopes`: the granted ones;
  - `Client`: `telemetry.ClientClass(user agent)` as today.

  The client's web address never enters `Identity`, labels or audit. Pages that show it look it up by grant id.
- **Scope checks:** the tool-level checks that exist today apply unchanged. A missing `coach:write` is a tool error, not a 403 step-up **[call]**; the spec's 403 `insufficient_scope` is a SHOULD.
- **Otherwise: 401** with `WWW-Authenticate: Bearer resource_metadata="<PUBLIC_URL>/.well-known/oauth-protected-resource/mcp", scope="coach:read coach:write"`, plus `error="invalid_token"` when a token was sent.
- **Auditing in builtin mode (I1):**
  - A request with no credentials (`missing`) is counted, never audited. Every MCP connection starts with one.
  - `invalid`, `expired` and `revoked` go through a global audit sampler: at most 60 rows an hour, then one `audit_suppressed` row carrying the count when the hour ends.
  - The same sampler covers failed login, enrolment and consent finishes.
  - Access mode and the private lane are unchanged.
- **Housekeeping:** `last_used_at` on the grant is updated at most once a minute.

## 7. Storage

One migration, `0009_oauth.sql` **[builder]** (exact columns):
- **`oauth_clients`:** integer id, `client_id` unique, kind (`cimd` or `dcr`), name, redirect_uris (JSON), metadata (JSON), `fetched_at`, `cache_until`, `created_at`.
  - CIMD rows are written on the first successful fetch.
  - DCR rows are written at the first consent.
- **`oauth_grants`:** id, client (FK), scopes, resource, `passkey_id`, `created_at`, `activated_at` (first code exchange), `last_used_at`, `revoked_at`.
- **`oauth_codes`:** `hash` primary key, grant (FK), redirect_uri as sent, whether redirect_uri was explicit, `code_challenge`, `expires_at`, `used_at`.
- **`oauth_tokens`:** `hash` primary key, grant (FK), kind, scopes, `expires_at`, `used_at`, `created_at`.

**Housekeeping** (the existing loop):
- delete codes and tokens a day after they expire;
- delete grants that have no live token and are older than 30 days;
- delete CIMD client rows with no grant that have been unused for 30 days.

**Restore** (`store.ApplyCarryForward`) deletes every OAuth grant, code and token, so every agent reconnects after a restore **[call]**. Client rows stay.

## 8. Security checklist (for the reviewers)

1. **Redirects:** none before the client and redirect_uri are validated, and none to the client without a session. Matching is exact; loopback ignores the port and nothing else.
2. **`next`** is parsed, rejected or re-serialised, and fuzzed (§4.2).
3. **PKCE** S256 only, with the exact lengths.
4. **Codes:** single use, 5 min; a fully matching replay revokes.
5. **Refresh:** rotation; reuse after the 60 s grace revokes.
6. **`iss`** is on every authorization response, errors included.
7. **CIMD:** fetched only for the signed-in runner, with the SSRF dialer of §5.2, uninstrumented.
8. **Consent:**
   - page: `html/template`, `frame-ancestors 'none'`, `no-store`;
   - shows the punycode host and the redirect host, with the domain-mismatch and loopback warnings;
   - all request fields come from the signed token;
   - the nonce is consumed in the grant transaction.
9. **Lockout resistance:** verified assertions bypass the failure buckets; `begin` is unlimited; DCR is stateless.
10. **Disk-fill resistance:** no audit row for credential-less requests, and sampling for the rest.
11. **Never in telemetry or audit targets:** tokens, codes, verifiers, client names, redirect URIs or CIMD URLs. The telemetry allowlist test covers the new code.
12. **Body caps:** every endpoint caps its body at 64 KiB; query strings stay within `MaxHeaderBytes`.
13. **URLs** come only from `COACHD_PUBLIC_URL`. Rate limits never key on the client IP.
14. **Token scope:** agent tokens are valid only on `/mcp`; the page never accepts a bearer token; the public `/mcp` never accepts `cdt_`.
15. **Default deny:** the page gate is default-deny, and the route walk test proves it.

## 9. Observability, audit, notification

- **Metric** `coachd.oauth.requests{endpoint, outcome}`:
  - `endpoint`: `metadata`, `register`, `authorize`, `consent`, `token`, `revoke`, and `other` for unknown paths;
  - `outcome`: `ok`, `invalid_request`, `invalid_client`, `invalid_grant`, `invalid_scope`, `invalid_target`, `access_denied`, `reuse`, `rate_limited`, `fetch_failed`, `login_required`, `not_found` (unknown path or wrong method) and `error` (coachd's own failure, a 5xx).
- **Dashboard:** one panel, OAuth requests by outcome.
- **Alerts:** no new alert **[call]**.
- **Audit events.** The target is an internal client or grant id, never a URL or name.
  - `oauth_consent`, outcome `approved` or `denied`;
  - `oauth_grant_revoked`;
  - `oauth_code_reuse`;
  - `oauth_refresh_reuse`;
  - `audit_suppressed`;
  - `auth_failure` (sampled, §6).
- **ntfy, when configured (M14).** The texts carry no names:
  - "A new app was connected to coachd", sent on the first code exchange of a grant;
  - "coachd revoked an app connection after a token was reused", sent on a reuse that revokes.

  Both link to `/apps`.
- **Logs:** internal ids, endpoint, outcome and error class only.

## 10. Deployment docs

A new `deploy/README.md` section, "Built-in auth (no Cloudflare Access)":
- **Settings:** `.env` gets `COACHD_PUBLIC_AUTH=builtin` and `COACHD_PUBLIC_URL=https://coachd.<domain>`; leave `COACHD_ACCESS_*` empty. Optionally set `COACHD_OAUTH_DCR=on`, explaining what it is for.
- **Exposing `127.0.0.1:8080`**, either way:
  - **Cloudflare Tunnel without an Access application.** Easiest; Cloudflare still sees the traffic decrypted at its edge. Bot Fight Mode and WAF challenges must not apply to `/mcp`, `/oauth/*` and `/.well-known/*`, or Anthropic's (`160.79.104.0/21`) and OpenAI's servers get challenged (M17).
  - **Caddy on the host** (automatic TLS, `reverse_proxy 127.0.0.1:8080`) plus a router port forward for 443. Encrypted end to end.
- **Connecting:** then `coachd enroll`, and connect Claude with `https://coachd.<domain>/mcp`.
- **Switching from Access mode:** delete the Cloudflare Access application for the hostname, or Access answers before coachd.

## 11. Testing and verification

**Unit tests** per endpoint cover every rule of §4–§6, including:
- a redirect mismatch never redirects;
- authorize without a session never fetches;
- duplicate parameters;
- `plain` PKCE;
- code and refresh reuse, inside and outside the grace;
- `resource` and scope rules;
- optional `redirect_uri` at the token endpoint;
- a revoked grant gives 401;
- a `cdt_` token on the public lane gives 401;
- the §3 mode matrix;
- OAuth state deleted at startup in Access mode.

**Lockout and abuse tests:**
- a saturated failure bucket still lets a valid login and a valid consent through;
- credential-less `/mcp` requests write no audit row;
- the sampler caps rows.

**Gate tests:**
- the default-deny route walk;
- the `next` fuzz cases;
- JSON 404 for unknown `/.well-known/*` and the legacy paths.

**CIMD tests** use a fetcher with an injectable resolver and dialer:
- each refused address class;
- a mixed record set;
- a redirect;
- an oversized body;
- a slow server;
- a `client_id` mismatch;
- non-https;
- an IDNA failure;
- the three live documents as fixtures.

The test-only override lives in an unexported field set by in-package tests, never in config or a build tag reachable from main (M18).

**End to end:** the MCP Go SDK's client side (`auth.AuthorizationCodeHandler` with `mcp.StreamableClientTransport`) runs against an `httptest` TLS server in builtin mode:
- discovery from the 401;
- CIMD from a test document server, and DCR when on;
- the runner's login and consent through the web tests' software authenticator;
- token, `tools/list`, refresh, revoke, then 401.

**Regression:** the existing Access-mode and private-lane tests still pass unchanged.

**Live** (owner, after merge):
1. Run builtin mode on the Mac with `COACHD_PUBLIC_URL=http://localhost:8080` and connect Claude Code. Also try two Claude Code sessions at once (the refresh grace).
2. Optionally switch the Pi to builtin mode for a claude.ai and ChatGPT check (§10), then choose which mode to keep.

## 12. Changes to existing documents

- **Main spec:**
  - §8 Approval step 2: the page is protected by Cloudflare Access or by a passkey session (builtin mode).
  - §9: the scopes bullet notes that builtin mode grants per client and the runner may grant read only. The client-attribution line notes per-client grants.
  - §10: the public-lane bullet gains "or coachd's built-in auth (builtin mode, this document)", and the Fallback bullet points here.
- **`CLAUDE.md`** (the owner edits it). The invariant "Public access only via Cloudflare Tunnel plus Access…" becomes:
  > Public access only through Cloudflare Access (access mode) or coachd's built-in OAuth and passkeys (builtin mode). In builtin mode only login, enrolment, OAuth metadata, authorize, token, revoke, register and `/device/v1` are reachable without a session or token, and each checks its own credential.

  The Status line and the Commands also need updating.

## 13. Left to the builder

- Package layout: protocol logic in a new `internal/oauth`, HTML in `internal/web` is the suggestion.
- Exact SQL; template wording and CSS.
- Whether to use the SDK's `oauthex` metadata types.
- The test fixtures.
- `golang.org/x/net/idna` and `golang.org/x/net/publicsuffix` are already in the module graph. Promoting `golang.org/x/net` to a direct dependency is expected.
