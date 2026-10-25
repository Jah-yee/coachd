# coachd: approve the week

Status: design, 2026-10-25. Extends `coachd-design.md` §8 (Approval). Tags as in the main design:
**[owner]** = owner decision, **[call]** = designer's call, **[builder]** = left to the implementer.

## 1. Intent

The owner plans a week at a time (2026-10-25). **[owner]** On Sunday the agent proposes the week's
five to seven workouts: two runs and Zwift on most other days. Today each one needs its own page and
its own passkey approval, so approving the week takes about seven passkey prompts in a row.

After this change:

1. The home page says "7 waiting" and links to **Review all**.
2. The review page shows every pending workout in full: day, name, steps, notes, the agent's
   rationale, warnings, and what each one replaces.
3. One **Approve all 7 with passkey** button runs one passkey approval, which approves exactly
   the workouts on the page, with the content shown, and nothing else.

Single approval stays as it is.

## 2. Goals, non-goals, anti-goals

**Goals**
- One passkey approval for every approvable pending workout, after the runner has seen each one in
  full.
- The same guarantees as a single approval: a fresh user-verified assertion, a single-use nonce, a
  binding to each proposal's ID and content hash, a date check, and audit rows.

**Non-goals**
- Approving a subset by ticking boxes. The runner declines the ones they do not want first, then
  approves the rest. **[call]** This keeps the signed set identical to what the page shows.
- Decline all.
- Approval from the watch, ntfy or any agent path. The invariant stands: approval needs a WebAuthn
  passkey on coachd's page.
- Fewer ntfy notifications. Each proposal still notifies; batching them is a separate change.

**Anti-goals**
- An approval that covers a workout the runner did not see, or content that changed after the page
  was rendered.
- A partial result the runner cannot see: either every workout on the page is approved, or none.
- Any widening of who can approve. The agent cannot start, finish or alter a batch approval.

## 3. The set

The approvable set is every proposal that:

- is `pending`;
- is intact (its stored bytes hash to its content hash);
- is dated today or later;
- is not replaced by another pending proposal. Approving the replacement supersedes it, so
  approving both in one go would conflict with itself. **[call]**

It is ordered by date, then creation time, and capped at 20. **[call]** The workouts left out are
listed under the set with the reason: date passed, content does not match its hash, or replaced by
another waiting workout. Over the cap, the extra workouts wait for the next round.

## 4. The ceremony

- A new ceremony purpose, `approve_all`. It is never accepted where `approve` is expected, nor the
  reverse.
- The signed ceremony carries the list of `(proposal ID, content hash)` in set order, at least one.
  The finish acts on that signed list only and takes no IDs from the browser, like consent. **[call]**
- It expires at the earlier of now + 24 h and the start of the day after the set's earliest date,
  in the runner's timezone: the single-approval rule, applied to the first workout.
- The invariant reads: approval nonces are single-use and HMAC-signed, bound to the proposal ID plus
  a content hash. A batch nonce is bound to every proposal ID plus its content hash in the set.
  It authorises exactly that set, once. **[call]**

## 5. Approving

`POST /review/approve` with `{ceremony, credential}`, as `/p/{id}/approve`:

1. Verify the assertion (`FinishAssert` with purpose `approve_all`); a used nonce, an expired page or
   a failed check answer as for a single approval.
2. For each bound proposal, reload it and check that its stored bytes still hash to the bound hash.
3. In one store transaction: consume the nonce, then approve each proposal in set order with the
   checks and the settling of replaced proposals that a single approval does. The first conflict
   (no longer pending, content changed, date passed, not found) rolls back everything, nonce
   included, and the page answers 409 naming the workout: "Tempo (Wed 28 Oct) is declined; reload
   the page". **[call]**
4. Then, as for a single approval: approval latency per proposal, the passkey's sign count, a
   session if there was none.

Audit: the usual `proposal_approved` row for each workout, and `proposal_withdrawn` or
`proposal_superseded` for each one it replaces. The client is the passkey. **[call]** No new audit
event.

## 6. Pages

- **Home:** when two or more workouts are waiting, a **Review all** link beside the count.
- **Workout page:** when other workouts are waiting too, a line "N more waiting · Review all".
- **Review page** (`GET /review`): the set in full, each as on its own page (no buttons per
  workout, a link to its page), then the button. Without a passkey it shows the enrolment hint;
  without any approvable workout it says so.
- The approve button reuses the page's existing approve script: it posts and reloads.

## 7. Telemetry

A span `proposal.approve_all` with `count` (workouts in the set) and `outcome`, linked to each
workout's proposing trace. No proposal IDs as attributes: the audit log holds them. No new keys.

## 8. Tests

- Passkey: the new purpose's binding check; an `approve` token refused for `approve_all` and the
  reverse; an empty list refused.
- Store: all approved in one transaction with one nonce; replay; any conflict rolls back every
  approval and the nonce; replaced proposals settled; audit rows.
- Web: the set rules (date passed, tampered, replaced by a pending one, the cap); the review page;
  the approve endpoint's success and every failure; the home and workout page links; the route walk.

## 9. Left to the builder

- Template layout and wording.
