# Clip Package Index

This package implements §3.2 of the IM quick-capture redesign: turning a
captured link into a body-only Markdown "web clip" under `Raw/Sources/`.

Flow: a bare-URL message in quick-capture mode reaches `Service.Clip`, which
writes a `status: clipping` skeleton note instantly (so the front end can ack
"已记录，剪藏中") and enqueues a `Job`. The background `Worker` drains the queue,
calls `Service.ProcessClip` to fetch + extract + rewrite the note to
`status: clipped` (or `clip-failed`), emits a result/error outbox message, and
re-uses the enrich queue to tag the finished clip (§4).

Module layout:

- `service.go`: `Service` (skeleton create + background process), `Config`,
  plan building, and the `EnrichEnqueuer` hook. All writes go through
  `VaultPlan -> policy -> executor` — never direct writes.
- `extract.go`: dependency-free HTML→Markdown readability extractor (stdlib
  tokenizer). First cut; expected to be swapped for a richer clip skill later
  (see docs §8).
- `fetch.go`: HTML fetch via the `internal/safehttp` SSRF-guarded client, with
  content-type and size limits.
- `render.go`: clip-note frontmatter/body builders and the filename slug.
- `queue.go`: in-process, non-durable hand-off. Durability comes from the
  worker's scan of `status: clipping` notes, not from this queue.
- `worker.go`: serial drain + periodic straggler recovery scan.

Boundaries:

- Outbound HTTP must go through `internal/safehttp` (SSRF guard). Do not add a
  second HTTP client here.
- Clip notes land only under `conventions.RawSourcesDir`; the low-risk policy
  `Check` allows `create_note`/`rewrite_note` there.

Authoritative contract: `docs/30-design/im-quick-capture.md` §3.2.
