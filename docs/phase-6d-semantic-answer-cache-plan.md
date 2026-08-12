# Phase 6d: Semantic answer cache

Fourth cost-optimization item, split out alongside [6a](phase-6a-prompt-caching-plan.md)/[6b](phase-6b-dedup-search-results-plan.md)/[6c](phase-6c-daily-question-cap-plan.md); see `docs/phase-6-cost-optimization-plan.md` for the shared context. Different in kind from 6a/6b, though: those are structural, zero-quality-tradeoff wins; this one is not.

## Context

6a and 6b never change what the model is asked to do, they only stop redundant token processing, so they can't produce a worse answer than not having them. This phase is different: it skips calling the model *at all* on a cache hit, returning a previously-generated answer for a *similar but not identical* new question. That's a real correctness risk, not a pure efficiency win, and it sits in tension with this Phase's own stated non-goal ("don't reduce answer quality to save money"). Worth being explicit about that tradeoff rather than building this quietly alongside 6a/6b as if it were the same kind of change.

The concrete risk: two questions can embed as very similar while having different correct answers, e.g. "how does health regen work" vs. "how did health regen work before the refactor," or two similarly-phrased questions about two different weapon classes that happen to share a lot of vocabulary. A false-positive cache hit doesn't fail loudly, it confidently returns a plausible-sounding wrong answer, which is a worse failure mode for a coaching tool than just answering slowly. This phase should ship with a genuinely conservative default (strict similarity threshold, feature flagged off by default) rather than tuned for maximum hit rate.

**Invalidation**: the codebase's index changes whenever `cmd/indexer` re-runs (on every merge, per how this is planned to be triggered), which means any previously-cached answer could reference now-outdated content. Selective invalidation (only flush answers whose citations touch changed files) needs tracking cache-entry-to-source-file dependencies, meaningfully more complex, and not worth building until the simple version is proven useful. **This phase takes the blunt, obviously-correct option instead: every reindex flushes the entire answer cache**, full or incremental. Even a `-changed` partial reindex touches files that could be referenced by any cached answer, and there's no cheap way to know which ones without that dependency tracking, so a full flush is the safe default, same reasoning `code_chunks` itself uses for exact search over an approximate index at this project's scale.

## Approach

**Reuses existing infrastructure, doesn't duplicate it**: both `cmd/api` and `cmd/indexer` already share `pkg/retrieval` (via the `go.work` `replace` directive) and already construct a `retrieval.Store` backed by the same Postgres instance and the same `EmbeddingDim` (1024, `pkg/retrieval/embedder.go`). The answer cache belongs there too, not in a `cmd/api`-only package, since `cmd/indexer` needs to call the flush method directly and shouldn't reach into `cmd/api`'s internal handler-layer packages.

**Schema**, added to `pkg/retrieval/store.go`'s existing `migrateSQL` (same place `code_chunks` is defined, so `cmd/indexer`'s existing `store.Migrate()` call at startup creates it too, no new migration mechanism):

```sql
CREATE TABLE IF NOT EXISTS chat_answer_cache (
    id         SERIAL PRIMARY KEY,
    question   TEXT NOT NULL,
    embedding  vector(%d) NOT NULL,
    answer     TEXT NOT NULL,
    citations  JSONB NOT NULL DEFAULT '[]',
    created_at TIMESTAMP DEFAULT NOW()
);
```

**New file `pkg/retrieval/answercache.go`**, methods on the existing `Store` type (same DB connection, no second pool):
- `LookupAnswer(embedding []float32, maxDistance float64) (*CachedAnswer, bool, error)` — nearest-neighbor via the same `embedding <=> $1` operator `Search` already uses, `ORDER BY embedding <=> $1 LIMIT 1`, hit only if the returned distance is within `maxDistance`. No hit at all if the table's empty or nothing's close enough.
- `StoreAnswer(question string, embedding []float32, answer string, citations []byte) error` — one insert, called after a fresh (non-cached) answer is generated.
- `FlushAnswerCache() error` — `TRUNCATE chat_answer_cache`, called by `cmd/indexer` after every successful index run (both `-full` and `-changed`).

**`chat.go` changes**: at the top of `Chat`, after validating the message (and after the Phase 4b usage increment, if that's landed by the time this is built), embed the question via the existing `h.embedder.EmbedQuery` (same embedder already used for `search_codebase`, no new provider) and call `LookupAnswer`. On a hit, return the cached `{answer, citations}` immediately, skipping the tool loop entirely, that's the entire cost saving, zero Claude tokens spent. On a miss, run the existing flow unchanged, then on success call `StoreAnswer` with the freshly generated answer before returning it.

**`cmd/indexer` changes**: after a successful run (both `-full` and `-changed` paths, wherever the existing "Done." log line is), call `store.FlushAnswerCache()`.

**Config**: `chat_answer_cache_enabled` (bool, **default false**, deliberately opt-in given the correctness tradeoff above) and `chat_answer_cache_max_distance` (float, no default asserted here, needs empirical tuning against this project's actual embedding model and question patterns before picking one, "must verify at implementation time" in the same spirit as 6a's cache-breakpoint-limit caveat).

## Files

- `pkg/retrieval/store.go` — add `chat_answer_cache` to `migrateSQL`
- `pkg/retrieval/answercache.go` (new) — `CachedAnswer`, `LookupAnswer`, `StoreAnswer`, `FlushAnswerCache`
- `cmd/api/lib/handlers/chat.go` — cache lookup before the tool loop, cache write on a fresh answer
- `cmd/api/config.local.example.yaml` — `chat_answer_cache_enabled`, `chat_answer_cache_max_distance`
- `cmd/indexer/main.go` — `FlushAnswerCache()` call after a successful run

## Verification

- Ask the same question twice; confirm the second call hits the cache (no `search_codebase` calls, near-instant response) and returns the same citations as the first.
- Ask two clearly different questions; confirm the second doesn't spuriously hit the first's cache entry.
- Ask two deliberately similar-but-different questions (e.g. the "before the refactor" example above); confirm the distance threshold is strict enough that this does *not* hit, tune `chat_answer_cache_max_distance` until it doesn't. This is the one check that actually matters most, a threshold that passes the first two checks but fails this one is not shippable.
- Run `cmd/indexer`, confirm `chat_answer_cache` is empty afterward even if it had entries before the run.
- Confirm `chat_answer_cache_enabled=false` fully bypasses both the lookup and the write, zero behavior change from today, so this can ship dark and be turned on deliberately once the threshold is trusted.
