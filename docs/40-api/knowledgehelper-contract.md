# KnowledgeHelper Contract Handoff — OpenWhisker Integration

> **Audience: the KnowledgeHelper (repo codename InterviewPolice, "KH") coding agent.**
>
> OpenWhisker ("OW") does **not** modify the KH codebase. This document is the
> contract OW needs KH to satisfy, expressed as **checkpoints**. Implement each on
> the KH side and tick it off (`- [ ]` → `- [x]`) as it lands. When a checkbox is
> ambiguous, propose a shape and flag it back rather than guessing.
>
> Design rationale (why any of this exists) lives in OW's
> [`knowledgehelper-integration.md`](../30-design/knowledgehelper-integration.md).
> This handoff is the actionable **KH-side slice only** — it does not restate the
> full design.

> **Status: draft, awaiting KH-side review.** The Part B DTO is an OW-proposed
> starting point; KH may refine field shapes and report back.

## 0. Scope & boundary

The integration splits ownership cleanly (full reasoning in design doc §2):

- **OW owns knowledge content.** KH question cards are a *projection* of OW vault
  notes — regenerable, never the second source of truth, never written back into.
- **KH owns review telemetry** (mastery, weak items, grading results, history).
  This is behavioral data the vault cannot produce on its own.

KH stays a **clean PWA + API**: no vault awareness, no OW dependency, no outbound
calls to OW. OW always pulls; KH only exposes.

**KH-side work is exactly two changes:**

| Part | Change | Weight |
|---|---|---|
| A | `Question.sourceRef` — carry an opaque OW id on each question | light (add a field) |
| B | A read-only review-telemetry endpoint OW can poll | medium (new endpoint + DTO) |

Everything else KH already has (`/import/*`, `/ai/grade`, `/progress`, `/history`)
is reused as-is.

---

## Part A — `Question.sourceRef`

### Why

For OW to route review telemetry back to the *originating vault note* (design doc
direction B), each KH question must carry a stable, **opaque** identifier that OW
stamped on it at import time. OW resolves that id back to a live note path on its
own side; KH never interprets it.

### Why not name it `source`

The name `source` is **already taken** in the spec with a different meaning —
`GradeResult.source` / `ExplainResult.source` (`enum: [ai]`, "this result was
AI-generated"). Reusing it would make codegen emit two same-named, different-meaning
fields. Use **`sourceRef`**: collision-free, neutral, and it does not leak that the
origin is a vault / OW (boundary requirement, design doc §11).

### Checkpoints

- [ ] Add optional field `sourceRef: { type: string }` to the `Question` schema in
      `openapi/openapi.yaml`.
- [ ] Regenerate both ends: `oapi-codegen` (Go backend) **and** `openapi-typescript`
      (web client). Confirm both type surfaces grow the field.
- [ ] Persist `sourceRef` on import (`/import/preview`, `/import/ai-preview`,
      `/import/commit` accept and store it) and echo it back on `/cards`.
- [ ] Treat the value as **opaque**: store and return it verbatim. Do not parse,
      validate against a format, or derive meaning from it.

### Acceptance

Import a question whose payload carries `sourceRef: "ow-q-abc123"` → read it back via
`/cards` → the same `sourceRef` is present and unchanged.

---

## Part B — Read-only review-telemetry endpoint

### Why

This is the single highest-value signal of the whole integration: the set of
questions the user **thought they knew but could not answer**, with the AI-graded
**`missing` points** (`GradeResult.missing`). OW pulls this periodically and turns
each into a Raw capture that drives knowledge gap-filling. A plain note system only
knows "what you wrote"; this endpoint lets the vault learn "what you can't actually
recall."

### Transport model (why it's a plain read endpoint)

OW polls this endpoint from its **scheduler**, whose charter is strictly read-only
with no outbound side effects. So:

- The endpoint is **read-only**. OW never writes back through the scheduler.
- **Increment via a `since` cursor**, not a server-side delivery flag. OW passes the
  last timestamp it saw; KH returns events at-or-after it.
- **Correctness is OW's job via idempotency.** OW dedups on a stable `eventId`, so
  re-fetching the same event is harmless. KH does **not** need per-event delivery
  tracking for v1 — see the optional checkpoints below.

### B.1 DTO draft (OW-proposed; KH may refine)

One row per question (not per attempt). Repeated wrong answers to the same question
**accumulate on `attempts`** rather than emitting new rows — downstream OW
enrichment does the topic aggregation, so keep these atomic.

```yaml
ReviewTelemetryEvent:
  type: object
  required: [eventId, sourceRef, status, attempts, lastReviewedAt]
  properties:
    eventId:        { type: string }                 # stable; OW dedups on this
    sourceRef:      { type: string }                 # echo of Question.sourceRef; opaque, do not interpret
    questionId:     { type: string }                 # KH-internal id (debugging / KH-side correlation)
    q:              { type: string }                 # question text, for human-readable Raw capture
    status:         { $ref: "#/components/schemas/QStatus" }   # reuse existing enum (weak/mastered/untouched)
    lastVerdict:    { type: string, enum: [correct, partial, wrong] }
    attempts:       { type: integer }                # cumulative count for this question
    missing:        { type: array, items: { type: string } }   # from GradeResult.missing — the core signal
    lastReviewedAt: { type: integer, format: int64 } # unix millis; also the cursor source
```

`sourceRef` (Part A) and `missing` (existing `GradeResult`) are the two fields that
make this row actionable on the OW side — everything else is context.

### B.2 Endpoint shape

```
GET /telemetry/review-events?since={unixMillis}&limit={n}
Authorization: Bearer <JWT>

200 →
{
  "events":   [ ReviewTelemetryEvent, ... ],   # status=weak, lastReviewedAt >= since, asc
  "nextSince": 1717900000000                    # pass back as ?since on the next poll
}
```

Reuse the existing single-user JWT (`/auth/login`); the endpoint sits behind the
same "backend is the sole data/AI egress" auth as the rest of the API.

### Checkpoints — must-have (v1)

- [ ] New read-only endpoint `GET /telemetry/review-events`, JWT-locked, in
      `openapi/openapi.yaml` + both codegen ends.
- [ ] Returns weak questions only, each carrying `missing` and the echoed `sourceRef`.
- [ ] `since`-cursor increment: caller passes last `nextSince`; response is ordered
      by `lastReviewedAt` ascending and includes the next cursor.
- [ ] One row per question; repeated wrong answers accumulate on `attempts`, no row
      explosion.

### Checkpoints — optional / later (do **not** block v1)

- [ ] Per-event `pending/delivered` tracking, so acked events are not re-sent. This
      is a **bandwidth optimization, not a correctness requirement** (OW idempotency
      already guarantees correctness). It is a genuinely new state layer on top of
      the snapshot-shaped `ProgressEntry` / append-shaped `HistoryRecord`, so scope
      it as real work if/when it's pulled in — not as "add a flag."

### Acceptance

Answer one question wrong (so `/ai/grade` produces a `missing` list) → poll
`GET /telemetry/review-events?since=0` → the response contains that event, with the
correct `missing` array and the `sourceRef` that was set in Part A.

---

## What KH must NOT do

- Do **not** read or write the vault. OW is the vault's only gateway.
- Do **not** call OW or push anything outbound. OW pulls; KH exposes.
- Do **not** interpret `sourceRef`. It is an opaque token owned by OW.
- Do **not** treat question cards as a content source of truth. They are a
  regenerable projection; OW may re-push and overwrite them, never the reverse.

## Codegen reminder

`openapi/openapi.yaml` is the single source of truth. Every schema/endpoint change
above must be followed by **both** `oapi-codegen` (Go) and `openapi-typescript` (web)
so the two type surfaces stay in lockstep. A change that regenerates only one side is
incomplete.
