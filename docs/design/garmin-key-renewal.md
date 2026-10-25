# coachd: renew the Garmin key on the page

Status: design, 2026-10-25. Extends `coachd-design.md` §5 (Data in). Tags as in the main design:
**[owner]** = owner decision, **[call]** = designer's call, **[builder]** = left to the implementer.

## 1. Intent

A Garmin Personal API key expires after 30, 90 or 180 days, or 1 year. Today renewal means signing
in to the host, replacing the Docker secret file, editing `COACHD_GARMIN_KEY_EXPIRES` and
restarting coachd. The owner's key expires on 2026-11-22. An open-source runner without SSH habits
meets the chore several times a year.

After this change, the runner creates a new key on Garmin's page, opens coachd's **Garmin key**
page, pastes the key, picks its validity and saves. coachd checks the key with Garmin and switches
to it at once, with no restart.

## 2. Goals, non-goals, anti-goals

**Goals**
- Replace the key from the page, behind a passkey session, checked with Garmin before use.
- The expiry everywhere (the `get_status` days left, the key-expiry metric and alert, the page)
  follows the key in use.

**Non-goals**
- Creating the key. Garmin's page does that; the Personal API has no endpoint for it.
- Removing the configured key. `COACHD_GARMIN_API_KEY_FILE` and `COACHD_GARMIN_KEY_EXPIRES` stay
  required for the first start.

**Anti-goals**
- The key on any page, in any log, metric, span, audit row or backup. The form never fills the
  field back in, and the page shows only the expiry.
- A broken key replacing a working one. Nothing changes unless Garmin accepts the new key.

## 3. Where the key lives

- The renewed key is written to `${COACHD_DATA_DIR}/garmin-key.json` (`{"key", "expires"}`), mode
  0600, atomically (temporary file, fsync, rename). It is read with the secret-file checks: a file
  readable by group or others is refused. **[call]**
- At start, coachd uses the configured key or the renewed one, whichever expires later. **[call]**
  So a runner who goes back to editing the secret file with a later expiry is not overridden.
- Backups cover the database and FIT files only, so the renewed key is not in them. **[call]**
  After a restore on another host, the configured key applies until the runner renews again.

## 4. Renewing

`GET /garmin` shows the key's expiry and days left, and a form behind a passkey session:

- **New key:** a password field with autocomplete off. Surrounding whitespace is trimmed. Empty,
  over 4 KiB, or containing spaces or control characters is refused before any Garmin call.
- **Valid for:** 30 days, 90 days, 180 days or 1 year, as chosen on Garmin's page. The expiry is
  today plus that, in the runner's timezone. **[call]** A select rather than a date field, so the
  runner cannot mistype the date.

`POST /garmin` (passkey session, CSRF protection as for every form):

1. The Garmin client exchanges the new key for a token while holding its token lock, so no other
   request uses a half-switched key.
2. If Garmin refuses (401 or 403), rate-limits (429), or cannot be reached, nothing changes and the
   page says which: "Garmin refused this key", "Garmin is limiting requests; try again later",
   "could not reach Garmin; nothing changed".
3. On success the file is written. Only then does the client switch to the new key and its token.
   If the write fails, the client keeps the old key and the page says so.
4. Audit: `garmin_key_renewed` with outcome `ok`, `refused`, `rate_limited`, `unreachable` or
   `error`. The client is the passkey. The audit row never holds the key.

The next scheduled sync uses the new key; nothing else restarts.

## 5. Home page

The home page gains a **Garmin key** line: "expires Sun 22 Nov (in 28 days)", linking to the page.
Within 14 days of expiry it shows as a warning. **[call]**

## 6. Telemetry

- A log line `garmin key renewed`, with `outcome` and `calendar_date` (the new expiry).
- The key-days-left gauge reads the key in use.
- No new keys.

## 7. Tests

- Holder: the later expiry wins at load; the file mode and atomic write; a group-readable file is
  refused; a corrupt file is an error, not a silent fallback.
- Client: a refused, rate-limited or failed exchange keeps the old key and token; success switches
  both; the commit failing keeps the old key.
- Web: the page needs a session to post; input checks; each outcome's message and audit row; the
  key never appears in a response body or log.
- Wiring: `get_status` and the gauge follow a renewal.

## 8. Left to the builder

- Page layout and wording.
