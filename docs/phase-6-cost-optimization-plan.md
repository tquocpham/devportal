# Phase 6: Chat cost optimization

## Context

Designers are expected to ask a lot of questions, that's the actual goal of this tool (guiding them to better code, not just occasional lookups), which means per-conversation cost adds up faster than originally budgeted for. Explicit non-goal for this phase: don't reduce answer quality to save money, no cheaper model, no lower `chat_max_iterations`/`chat_top_k`. Those are real levers but they trade away the thing that matters most for a coaching tool. Everything here is structural efficiency: same answers, less wasted spend.

**The intended product shape**: the company-paid web chat is the default, free-to-the-user tier, but it isn't meant to be unlimited. A user who consistently exceeds it is expected to graduate to bringing their own Claude account, and from that point on uses the codebase via the Phase 4 MCP server instead of this app's chat UI. That handoff is meant to be a *success path*, not a cutoff, someone who hits the cap should be pointed at exactly how to keep going effectively on their own account, not just told no. Phase 4 (MCP server) ships ahead of this phase specifically so that's already true by the time any of this lands.

Four independent, additive changes, ranked by leverage, each now its own doc since they ship independently and touch different code. The first three are structural efficiency with no quality tradeoff; the fourth is a different kind of change, worth reading its own Context section before assuming it belongs in the same bucket.

## [Phase 6a: Incremental prompt caching in the tool loop](phase-6a-prompt-caching-plan.md)

The biggest lever. Every iteration of the search loop currently re-prices the entire growing conversation as fresh input tokens; moving the cache breakpoint forward each turn fixes that.

## [Phase 6b: Dedup tool-result content within a single turn](phase-6b-dedup-search-results-plan.md)

Small, free win. Citation dedup already stops a chunk from being *listed* twice; this stops its full text from being *sent* twice too.

## [Phase 6c: Generous per-user daily question cap, with a graduation path attached](phase-6c-daily-question-cap-plan.md)

Safety net, not a quality lever. Depends on [Phase 4b](phase-4b-chat-usage-tracking-plan.md)'s usage tracking (already scoped separately, since visibility into usage is useful on its own, independent of ever adding a cap), this phase adds the threshold check and the graduation-to-MCP messaging on top of that.

## [Phase 6d: Semantic answer cache](phase-6d-semantic-answer-cache-plan.md)

Different in kind from the three above: skips calling the model entirely on a cache hit for a *similar, not identical* question, which is a real correctness risk in exchange for cost savings, not a pure efficiency win. Ships feature-flagged off by default; see its own Context section for why.

## Verification

See each sub-doc's own Verification section, they're independent enough to test in isolation.
