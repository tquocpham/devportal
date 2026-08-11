# Phase 4: Expose codebase search via a remote MCP server

## Context

This is the original idea from the very start of the project — an MCP server — set aside early on because MCP means whoever's Claude session calls the tool pays for those tokens, and the goal then was centralized, company-paid billing (one shared Anthropic key, `docs/phase-2-chat-api-plan.md`). Now that team members are expected to bring their own personal Claude accounts/subscriptions, that constraint is gone: MCP becomes the right shape again. Their own Claude session (Claude Code today, potentially claude.ai/Desktop later) grounds itself in the indexed codebase at *their own* token cost, reusing the exact retrieval capability already built and working — `search_codebase` in `chat.go` — instead of duplicating it.

**Moved ahead of code review and cost optimization** (was Phase 6, now Phase 4) — it's the prerequisite for both of those, not an independent nice-to-have: Phase 6 (cost optimization)'s usage cap hands off to this MCP server once someone outgrows the free tier, and that handoff only means something once this actually exists. Building it first turns what used to be a forward reference ("depends on Phase 6 existing") into a normal backward one.

**This isn't a standalone alternative to the web chat — it's the intended graduation path from it.** The product shape is two tiers: the company-paid web chat is the default, generous-but-capped tier (Phase 6's daily limit); once someone consistently outgrows that cap, they're expected to bring their own Claude account, and from that point on this MCP server is what keeps them just as effective as they were on the company plan — same indexed codebase, same grounded answers, now billed to them instead of the company. Phase 6's cap-exceeded response is meant to point directly at this phase's setup instructions, not just block the user.

**Real gap worth naming, not glossing over**: the only confirmed-working client for this phase is Claude Code — a terminal tool. That's a fine bar for developers, but the audience that actually hits the daily cap hardest is likely designers (the whole reason this app has a guided web UI instead of just handing everyone Claude Code from day one, per the very first design decision in this project). Good setup docs narrow that gap but don't fully close it — running `claude mcp add ...` in a terminal is a materially higher bar than clicking through a web chat, independent of how clear the instructions are. Worth deciding explicitly whether that's acceptable (developers graduate cleanly, designers who hit the cap either stay capped or need a human's help to get set up) or whether claude.ai's connector UI — a normal web flow, no terminal — should actually be prioritized *ahead of* Claude Code for this audience, despite being the bigger OAuth lift. Flagging this as a real open decision, not resolving it here.

Researched against real, current sources before writing this (not assumed from training data, since MCP's spec and client support are exactly the kind of thing that drifts):

- Official Go SDK: [`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk) (`mcp` package), Google-maintained, current on spec version 2026-07-28.
- [`mcp.NewStreamableHTTPHandler`](https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp) returns a plain `http.Handler` — mountable directly into the existing `cmd/api` Echo app, same process, same Tailscale-reachable port. No new binary, no new deployment surface.
- Claude Code supports remote MCP servers with simple bearer-token auth today: `claude mcp add --transport http devportal <url> --header "Authorization: Bearer <token>"` (confirmed current syntax).
- claude.ai's own custom-connector UI (Customize → Connectors → Add custom connector) is confirmed available to Pro/Max personal accounts, not just Team/Enterprise — but its auth path appears to require real OAuth 2.1 (client ID/secret), not a bearer token, per a live upstream issue on this exact gap. Materially more work than the Claude Code path.

Given that asymmetry, this phase scopes to **Claude Code only**. claude.ai/Desktop support is called out as a deliberately deferred stretch goal below, not bundled in — it needs its own research pass on the OAuth 2.1 flow (the MCP spec's July 2026 revision also deprecates Dynamic Client Registration in favor of Client ID Metadata Documents, which would need to be re-verified against current docs at that time, not assumed from what's written here).

## Approach

**New dependency**: `github.com/modelcontextprotocol/go-sdk/mcp`.

**New file `cmd/api/lib/mcp/server.go`** (or similar) — builds an `mcp.Server` and registers one tool:

```go
type searchArgs struct {
    Query string `json:"query" jsonschema:"required,description=..."`
}

mcp.AddTool(server, &mcp.Tool{
    Name:        "search_codebase",
    Description: "...", // same description already used for the chat tool
}, func(ctx context.Context, req *mcp.CallToolRequest, args searchArgs) (*mcp.CallToolResult, any, error) {
    // same embedder.EmbedQuery + store.Search logic already in chat.go's
    // search_codebase closure — factor the retrieval body itself into a
    // small shared function both this and chat.go/searchtool.go call, so
    // there's exactly one implementation of "search the index and format
    // results," not two copies that can drift.
})
```

**Auth — reuse existing JWT infra, don't build a second auth system.** A new self-service endpoint, `POST /api/v1/me/mcp-token` (protected, same as everything else), mints a JWT the same way `AssumeRole` already does (`cmd/api/lib/handlers/admin_assume_role.go` is the direct template) — same signing key, same claim shape (`sub`, `exp`) — but long-lived (weeks/months, configurable) instead of 15 minutes, and with no `role` claim needed since it's not testing a role, just proving identity.

The MCP HTTP handler's `getServer func(*http.Request) *Server` callback reads the `Authorization: Bearer <token>` header, validates the JWT signature/expiry (identical logic to `mw.RequireAuth`), and **additionally re-checks `users.Store.Lookup(username)` on every single call** — unlike the 8-hour web session, which doesn't re-verify against the database per request. This matters because MCP tokens live much longer than a web session: if an admin revokes someone's platform access, their MCP access needs to die immediately, not whenever the long-lived token happens to expire. Re-checking `Lookup` per call gets that for free, reusing the exact allowlist that already exists — no new revocation list or token-storage table needed.

**Honest limitation to note, not hide**: a leaked long-lived MCP token can't be individually revoked before its natural expiry purely on its own — the mitigation is that removing the user from `allowed_users` kills it immediately anyway (via the `Lookup` re-check), which covers the realistic case (an admin needs to cut someone off). The only gap is a token that leaks *while the user is still legitimately allowed* — rotating `jwt_secret` is the blunt-but-effective fallback there, and it invalidates every session/token app-wide, not just the one leaked token. Worth deciding if that's acceptable or if a real per-token revocation table becomes necessary later; not building that now since it's speculative complexity for a risk that hasn't materialized.

**Mounting into the existing app**: register the MCP handler on the existing Echo instance in `main.go`, alongside the current routes — needs checking the installed Echo version's exact mechanism for wrapping a standard `http.Handler` (e.g. `echo.WrapHandler`) rather than assuming the API shape; same "verify against real source" discipline used for the Anthropic SDK research earlier in this project.

## Files

- `cmd/api/go.mod` — add `github.com/modelcontextprotocol/go-sdk`
- `cmd/api/lib/mcp/server.go` (new) — MCP server construction, `search_codebase` tool registration
- `cmd/api/lib/handlers/chat.go` / `searchtool.go` — factor the actual retrieval body (embed query → search → format) into a function both the chat tool and the MCP tool call, rather than duplicating it
- `cmd/api/lib/handlers/mcp_token.go` (new) — `POST /api/v1/me/mcp-token`, mirroring `admin_assume_role.go`'s minting logic
- `cmd/api/main.go` — construct the MCP server, mount its handler, register the token-minting route
- `cmd/api/README.md` — document how a developer connects Claude Code to it (the `claude mcp add ...` command, where to get a token)

## Deferred / stretch goal

**claude.ai and Claude Desktop custom-connector support.** Confirmed technically possible (personal Pro/Max accounts can add custom remote connectors), but the auth path is real OAuth 2.1 — a resource-server implementation, a `/.well-known/oauth-protected-resource` endpoint, PKCE, and (per the newest spec revision) Client ID Metadata Documents rather than Dynamic Client Registration. That's a meaningfully bigger scope than the bearer-token path this phase covers, and deserves its own research-and-plan pass rather than being estimated from what's written here — re-verify against current docs when it's actually prioritized.

## Verification

- Start `cmd/api`, mint a token via `POST /api/v1/me/mcp-token`, run `claude mcp add --transport http devportal http://<tailscale-host>:3000/mcp --header "Authorization: Bearer <token>"` from a real Claude Code session, confirm `/mcp` shows it connected.
- Ask Claude Code a question that should trigger `search_codebase`; confirm it returns real chunks from the index with correct file/line citations — same content the web chat would return for the same query.
- Revoke the test user via the admin panel (`DELETE /api/v1/admin/users/:username` or set them to no longer allowed) and confirm the *next* MCP call fails immediately, without waiting for the token's natural expiry — this is the specific behavior the per-request `Lookup` re-check is there to guarantee.
- Confirm a request with no `Authorization` header, and one with a garbage/expired token, both get a clean rejection rather than a crash or a silent fallback to "allowed."
