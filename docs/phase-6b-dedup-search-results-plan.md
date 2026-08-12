# Phase 6b: Dedup tool-result content within a single turn

Split out of `docs/phase-6-cost-optimization-plan.md`'s item B; see that doc for the shared cost-optimization context this belongs to. Small, free win, independent of Phase 6a.

## Context

**Problem**: `search_codebase`'s citation dedup (the `seen` map already in `chat.go`) stops the same `file:line` from being *listed twice* in the response, but doesn't stop the full chunk *text* from being sent to the model twice if two searches in the same turn return overlapping chunks.

## Approach

Reuse that same `seen` map when building each search call's result text, for a chunk already returned earlier in this turn, replace the full body with a one-line "(already shown above)" marker instead of repeating it.

## Files

`cmd/api/lib/handlers/chat.go` (the `search_codebase` tool closure), same future home as Phase 6a once extracted to `searchtool.go`.

## Verification

Craft two search queries in one turn likely to return overlapping chunks; confirm the second occurrence is replaced with the "already shown" marker rather than full text.
