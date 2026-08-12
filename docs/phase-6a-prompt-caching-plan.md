# Phase 6a: Incremental prompt caching in the tool loop

Split out of `docs/phase-6-cost-optimization-plan.md`'s item A; see that doc for the shared cost-optimization context this belongs to.

## Context

**Problem**: only the system prompt is cached today (`chat.go`'s `CacheControl` on the system block). Every iteration of the search loop (up to `chatCfg.MaxIterations`) re-sends and re-prices the *entire* growing conversation, history, the question, and every prior search result, as fresh input tokens, even though most of it is identical to the previous iteration. This is the single biggest lever of the three cost-optimization items: same answers, less wasted spend, no quality tradeoff.

## Approach

Switch `ChatHandler.Chat` from `runner.RunToCompletion(ctx)` to driving the loop manually via repeated `runner.NextMessage(ctx)` calls, moving a cache breakpoint to the end of the message list after each one. Confirmed against the installed SDK source (`betatoolrunner.go`), not assumed:

- `BetaToolRunner` embeds `betaToolRunnerBase`, whose `Params BetaToolRunnerParams` field is exported specifically so callers can inspect/mutate it (`betatoolrunner.go:39`, doc comment says as much).
- `NextMessage` reads `r.Params.Messages` fresh at the top of every call (`betatoolrunner.go:333-368`), so mutating `runner.Params.Messages` between calls is a supported pattern, not reaching into internals.
- Both `BetaTextBlockParam` and `BetaToolResultBlockParam` have a `CacheControl BetaCacheControlEphemeralParam` field (`betamessage.go:8583-8591` and `:10251-10260`), so a breakpoint can be set on whichever block type ends up last, whether that's the model's own text or a tool result `executeTools` appended.

Loop shape:

```go
var lastMsg *anthropic.BetaMessage
for {
    msg, err := runner.NextMessage(ctx)
    if err != nil { ... }
    if msg == nil {
        break // conversation complete
    }
    lastMsg = msg
    moveCacheBreakpoint(runner) // sets CacheControl on the last block of runner.Params.Messages, clearing whichever block we marked last iteration
}
answer := extractText(lastMsg)
```

**Must verify at implementation time, not assumed here**: the current per-request cache-breakpoint limit (moving exactly one breakpoint forward each iteration, rather than stacking a new one every turn, is the safe approach regardless of the exact limit, but confirm against live Anthropic docs before writing the final version, this project's existing practice is to never guess API behavior that might have drifted).

## Files

`cmd/api/lib/handlers/chat.go`. Once Phase 5 (code review) exists, `review.go` should reuse the same loop helper rather than duplicating it, worth extracting into `searchtool.go` alongside the already-shared `SearchCodebase`.

## Verification

Ask a question that triggers 2+ searches; check `response.usage.cache_read_input_tokens` on the later iterations, same technique already used in Phase 2 to confirm system-prompt caching was hitting, now extended to the growing conversation.
