# CLAUDE.md

Guidance for Claude Code when working in this repository.

## What this is

A **fork** of [EverythingSuckz/TG-FileStreamBot](https://github.com/EverythingSuckz/TG-FileStreamBot) (Go). It diverged
significantly from upstream and serves a different purpose: it is the **streaming backend for a separate Next.js app**,
delivering videos and images. Upstream is a general-purpose "instant stream link" bot — do not assume upstream changes
are improvements here without measuring (see Performance Investigation below).

Fork-specific additions vs upstream: Firebase-only stream auth, the `/direct` route, image caching, worker
metrics + load-balanced bot selection, race-based metadata fetch.

## Build & run

```sh
go build -o fsb ./cmd/fsb     # build
./fsb run                     # run (reads config from fsb.env)
./fsb session                 # session helper command
go build ./...                # compile check
go vet ./...                  # static analysis
```

Config is loaded from `fsb.env` (see `fsb.sample.env` for the documented template). The server runs the main
streaming API on `PORT` (8000 locally) and a separate status server on `STATUS_PORT` (8001 locally). Startup
brings up ~25 worker bots + 1 default bot — allow ~20s and watch the log for `Successfully started 25/25 bots`.

`fsb.env`, `fsb.session`, `thumbnails/` are gitignored. Generated benchmark JSON in `scripts/` is also gitignored.
`sessions/` (per-worker session files) are gitignored via the `*.session*` rule.

`./fsb healthcheck --msg <id> [--session-dir sessions]` probes every bot in `fsb.env` for live channel
access (offline tool; cold in-memory sessions yield false `CHANNEL_INVALID`, so use `--session-dir` for a
faithful test — but it can't run while the server holds the SQLite session files).

## Architecture

- `cmd/fsb/` — CLI entrypoint (`run`, `session`, `healthcheck` commands via cobra).
- `config/config.go` — env-based config (`envconfig`), defaults as `defaultXxx` consts mirrored into `ValueOf`.
  `MultiTokens` is loaded from `MULTI_TOKEN<N>` env vars and **sorted by the numeric suffix** so the token
  order (and the worker ID ↔ `worker-N.session` mapping) is deterministic across restarts.
- `internal/bot/` — `gotgproto` clients. `workers.go` holds the worker pool, metrics, and load-balanced
  `GetNextWorker()` (scores by active + total requests, plus an `unhealthyPenalty()` that deprioritizes
  workers with recent metadata-fetch failures). `Add(token, idx)` maps token index → worker ID `idx+1` →
  `worker-{idx+1}.session` deterministically.
- `internal/routes/` — Gin HTTP handlers:
  - `GET /stream/:messageID` — legacy hash-authed stream (`stream.go`).
  - `GET|HEAD /direct/:messageID` — Firebase-session-authed stream from `MEDIA_CHANNEL_ID` (`direct.go`).
  - `GET /thumb/:messageID` — thumbnails with local image cache (`thumb.go`, `image_cache.go`).
  - `POST|GET /auth/firebase/exchange` — exchanges a Firebase ID token for a short-lived stream session token.
  - `GET /status` — passive worker metrics (incl. fetch-health columns); JSON by default, HTML on
    `?format=html`. On the separate status server (also on main router via reflection).
  - `GET /health/bots?msg=<id>` — active in-process probe: fetches a known message with every bot and
    reports per-bot channel reachability (`health.go`). Use this to confirm "are all bots ok?" live.
- `internal/utils/reader.go` — `telegramReader`: streams a byte range from Telegram in 1 MB chunks with
  1-deep read-ahead prefetch. This is the **hot path** for streaming.
- `internal/streamauth/` — Firebase token verification + stream session store.
- `internal/cache/` — in-memory `freecache` for file metadata.

## Conventions

- Telegram API calls in stream handlers use `context.Background()` (not the request context) deliberately, so a
  client disconnect doesn't abort an in-flight fetch. (Trade-off: wasted work on player seeks — see below.)
- `MEDIA_CHANNEL_ID` / `LOG_CHANNEL` raw IDs are normalized via `stripInt` at load time.
- Don't commit `git` changes without explicit user approval.

## Performance investigation (status — paused, resume on the weekend)

Goal: improve streaming performance for the Next.js video/image backend.

### Key finding (2026-05-14)

**Telegram serializes `upload.getFile` RPCs per bot connection.** Splitting one stream's range into N parallel
sub-fetches *on the same bot* does not parallelize — the requests queue, and you pay N× the per-RPC overhead
(~300-370 ms each).

Evidence: ported upstream's `StreamPipe` (parallel block prefetch, originally from teldrive) into `/stream` and
`/direct`, benchmarked before/after on localhost with the 25-bot pool. Clear regression — **reverted**:

| Metric        | Before (current serial reader) | After (StreamPipe) |
|---------------|--------------------------------|--------------------|
| TTFB avg      | 1359 ms                        | 2536 ms            |
| TTFB min      | 482 ms                         | 1426 ms            |
| Throughput avg| 1.36 MB/s                      | 0.51 MB/s          |

Smoking gun: a single 1 MB request on an *idle* bot (`ramp_c1`, zero concurrency) went 636 ms → 1477 ms just from
being split into 4×256 KB "parallel" fetches.

Implications:
- A single bot connection has a hard serial download ceiling (~1.5–2 MB/s). In-connection parallelism can't beat it.
- The current `internal/utils/reader.go` is already near-optimal for single ~1 MB range requests.
- The system already scales for *concurrent* streams across the bot pool (baseline `ramp_c12` ≈ 21 MB/s aggregate).
- Upstream's `StreamPipe` pins one stream to one bot, so it hits the same wall — measure before adopting.

### Benchmark how-to

`scripts/test_concurrent_performance.py` is the streaming benchmark; `scripts/analyze_results.py <file.json>`
summarizes a result file. Run non-interactively against localhost:

```sh
printf '\n<FIREBASE_PASSWORD>\n' | python3 scripts/test_concurrent_performance.py \
  --base-url http://localhost:8000 --email <FIREBASE_EMAIL> \
  --messages 479688,479689,479691,479686,479693,479695,479697
```

First piped line = empty (accepts base-url default), second = password (read by `getpass` from stdin). Firebase API
key auto-loads from `fsb.env`. Test credentials are in `fsb.env` as `FIREBASE_EMAIL_TEST` / `FIREBASE_PASSWORD_TEST`.
Results auto-save to `scripts/baseline_localhost-8000_..._seqNNN.json`.

**Caveat:** the benchmark only ever issues 1 MB range requests (`FIXED_CHUNK_SIZE` hardcoded). It does not exercise a
large continuous transfer (e.g. `Range: bytes=0-` on a 700 MB file). Confirm the real Next.js player's request
pattern (small seek ranges vs. whole-file) before sizing further work.

### Next steps (ordered by payoff)

1. **Multi-bot chunk striping** — the only real lever for single-stream speed: fetch consecutive chunks of one
   stream round-robin across *different* bots (each bot = separate connection/account = genuine parallelism).
   Complex: `file_reference` and channel access are per-bot, plus chunk ordering. Decide scope after confirming
   the Next.js player's request pattern.
2. **Dependency updates** — `gotd/td` 0.139→0.144, `gin` 1.9.1→1.12 (this fork is already ahead of upstream on
   `gotd/td`). Low risk, measurable; run the benchmark after.
3. **`/direct` tuning** — `DIRECT_RACE_WORKERS=4` races 4 workers just for metadata; the metadata cache key
   includes the bot's `Self.ID`, diluting cache hits across the round-robin worker pool. Reduces wasted API
   calls; does not change throughput.

### Minor issues noted (not yet addressed)

- `/stream` uses plain `io.CopyN` (32 KB buffer); `/direct` uses a pooled 256 KB buffer — inconsistent.

### Bot health & "trava do nada" (addressed 2026-06)

Bots silently lost channel access while `/status` still showed them green (metadata-fetch failures were
never counted, and the load balancer *preferred* a dead bot for having a low total). Fixes shipped:
- Per-worker `MetadataFailures` / `ConsecutiveFailures` / `RacesLost` counters; `GetNextWorker`
  deprioritizes degraded workers (recovers on first success). Surfaced in `/status` "Fetch Health" column.
- `GET /health/bots` active probe (above) and the `fsb healthcheck` CLI.
- Deterministic `MULTI_TOKEN<N>` sorting + `worker-{idx+1}.session` mapping (above), which fixed duplicate
  bot identities / wasted tokens across restarts. Regenerating sessions (`rm sessions/*.session` + restart)
  is safe — the running server repopulates the channel `access_hash` from updates within seconds.

None of these touch the streaming hot path (`reader.go` / the copy loop) — they are pre-stream worker
selection and observability only.
