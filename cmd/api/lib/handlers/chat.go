package handlers

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/toolrunner"
	"github.com/devportal/api/lib/usage"
	"github.com/devportal/retrieval"
	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
)

const (
	chatSystemPrompt = `You are a coding assistant helping game designers (technical, but not
programmers) understand and write code for this Unreal project. Explain
plainly, avoid unnecessary jargon, and always cite the file and line range
you're drawing from.

You have a search_codebase tool. Use it to look things up. Call it more
than once if the first search doesn't settle the question. In particular:
  - Config and build files (e.g. DefaultEngine.ini, the .uproject file) are
    the source of truth for what's actually active in the project. Code
    comments and class names can describe unused or template content, so for
    "what is this project" / "what's active" style questions, search for the
    relevant config file specifically rather than trusting the first result.
  - If you find multiple parallel implementations of the same thing (e.g.
    several "variant" folders), that's a signal to check which one is wired
    up, not something to average together.

If your searches genuinely don't turn up the answer, say so directly rather
than guessing.`
)

// ChatConfig holds the cost/quality knobs for chat generation. All of these
// are exposed as config keys (see main.go) so they can be tuned without a
// code change; the values here are just the defaults.
type ChatConfig struct {
	Model         anthropic.Model
	MaxTokens     int64
	TopK          int
	MaxHistoryLen int // server-side floor, regardless of what the client sends
	MaxIterations int // caps Claude round trips per question, so search can't run away
}

func DefaultChatConfig() ChatConfig {
	return ChatConfig{
		Model:         anthropic.ModelClaudeSonnet5, // cost-conscious choice. See docs/phase-2-chat-api-plan.md
		MaxTokens:     2048,
		TopK:          6,
		MaxHistoryLen: 6,
		MaxIterations: 4,
	}
}

type ChatHandler interface {
	Chat(c echo.Context) error
}

type chatHandler struct {
	store     *retrieval.Store
	embedder  retrieval.Embedder
	anthropic anthropic.Client
	usage     usage.Store
	cfg       ChatConfig
}

func NewChatHandler(
	store *retrieval.Store, embedder retrieval.Embedder, anthropicClient anthropic.Client,
	usageStore usage.Store, cfg ChatConfig) ChatHandler {

	return &chatHandler{
		store:     store,
		embedder:  embedder,
		anthropic: anthropicClient,
		usage:     usageStore,
		cfg:       cfg,
	}
}

type chatMessage struct {
	Role    string `json:"role"` // "user" or "assistant"
	Content string `json:"content"`
}

type chatRequest struct {
	Message string        `json:"message"`
	History []chatMessage `json:"history"`
}

type citation struct {
	File      string `json:"file"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
}

type chatResponse struct {
	Answer    string     `json:"answer"`
	Citations []citation `json:"citations"`
}

// searchInput is the schema for the search_codebase tool, reflected into a
// JSON schema by toolrunner.NewBetaToolFromJSONSchema via the jsonschema tags.
type searchInput struct {
	Query string `json:"query" jsonschema:"required,description=A focused search query. e.g. a filename\\, class name\\, or specific concept to look up in the indexed codebase."`
}

// Chat handles POST /api/v1/chat (registered under the `protected` route group
// in main.go, so RequireAuth has already validated the session).
func (h *chatHandler) Chat(c echo.Context) error {
	var req chatRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if strings.TrimSpace(req.Message) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "message is required")
	}

	// RequireAuth (protected group middleware) guarantees this claim exists
	// and is valid by the time we get here, same guarantee AssumeRole relies
	// on without an ok check.
	claims := c.Get("user").(jwt.MapClaims)
	username, _ := claims["sub"].(string)

	// Fatal, unlike AddTokens below: this runs before the Anthropic call, so
	// failing here costs nothing extra, no tokens have been spent yet. If we
	// silently continued instead, we'd be about to spend real money on a
	// request that goes completely untracked, the exact blind spot this
	// feature exists to close, especially once Phase 6c starts enforcing
	// caps against this same data. Placed before the tool loop starts so a
	// later threshold check doesn't need to move either.
	if err := h.usage.Increment(username); err != nil {
		c.Logger().Errorf("increment chat usage for %s: %v", username, err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to record usage")
	}

	history := req.History
	if len(history) > h.cfg.MaxHistoryLen {
		history = history[len(history)-h.cfg.MaxHistoryLen:]
	}

	ctx := c.Request().Context()

	// citations accumulates across every search_codebase call the model
	// makes this turn, not just one fixed pre-fetch.
	var citations []citation
	seen := map[string]bool{}

	searchTool, err := toolrunner.NewBetaToolFromJSONSchema[searchInput](
		"search_codebase",
		"Search the indexed codebase (source + config files) for content relevant to a query. Returns the closest-matching chunks with their file paths and line ranges. Call this as many times as you need. e.g. search again with a different, more specific query if the first result doesn't settle the question.",
		func(ctx context.Context, in searchInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			text, chunks, err := SearchCodebase(h.embedder, h.store, in.Query, h.cfg.TopK)
			if err != nil {
				return anthropic.BetaToolResultBlockParamContentUnion{}, err
			}

			for _, ch := range chunks {
				key := ch.RelPath + ":" + strconv.Itoa(ch.StartLine) + "-" + strconv.Itoa(ch.EndLine)
				if !seen[key] {
					seen[key] = true
					citations = append(citations, citation{File: ch.RelPath, StartLine: ch.StartLine, EndLine: ch.EndLine})
				}
			}

			return anthropic.BetaToolResultBlockParamContentUnion{OfText: &anthropic.BetaTextBlockParam{Text: text}}, nil
		},
	)
	if err != nil {
		c.Logger().Errorf("build search_codebase tool: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to set up search")
	}

	runner := h.anthropic.Beta.Messages.NewToolRunner(
		[]anthropic.BetaTool{searchTool},
		anthropic.BetaToolRunnerParams{
			BetaMessageNewParams: anthropic.BetaMessageNewParams{
				Model:     h.cfg.Model,
				MaxTokens: h.cfg.MaxTokens,
				System: []anthropic.BetaTextBlockParam{{
					Text:         chatSystemPrompt,
					CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
				}},
				Messages: buildMessages(history, req.Message),
				OutputConfig: anthropic.BetaOutputConfigParam{
					Effort: anthropic.BetaOutputConfigEffortMedium,
				},
			},
			MaxIterations: h.cfg.MaxIterations,
		},
	)

	// Driving NextMessage manually instead of runner.RunToCompletion(ctx):
	// RunToCompletion only hands back the final message, discarding every
	// intermediate round-trip's Usage, which would silently undercount any
	// question that took more than one search. Each iteration is a
	// separate, real API call except the very last NextMessage call, which
	// re-returns the same *BetaMessage pointer a second time purely to
	// signal "done" (confirmed against betatoolrunner.go's executeTools:
	// no tool_use blocks means no new API call, r.lastMessage is returned
	// unchanged). Comparing pointers, not just non-nil, avoids
	// double-counting that final message's tokens.
	var resp, prevMsg *anthropic.BetaMessage
	var totalInputTokens, totalOutputTokens int64
	for {
		msg, err := runner.NextMessage(ctx)
		if err != nil {
			c.Logger().Errorf("anthropic tool runner: %v", err)
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to generate response")
		}
		if msg == nil {
			break
		}
		if msg != prevMsg {
			totalInputTokens += msg.Usage.InputTokens
			totalOutputTokens += msg.Usage.OutputTokens
			prevMsg = msg
		}
		resp = msg
	}

	// Non-fatal, unlike Increment above: the Anthropic call already
	// succeeded and already cost real money by this point, failing the
	// request now wouldn't undo that spend, it would just also deny the
	// user the answer they already paid for. Logging and returning it
	// anyway is strictly better than failing here.
	if err := h.usage.AddTokens(username, totalInputTokens, totalOutputTokens); err != nil {
		c.Logger().Errorf("add chat token usage for %s: %v", username, err)
	}

	return c.JSON(http.StatusOK, chatResponse{
		Answer:    extractText(resp),
		Citations: citations,
	})
}

// buildMessages converts trimmed history plus the new question into the
// []anthropic.BetaMessageParam the tool runner expects. Retrieved context no
// longer gets pre-fetched and injected here. The model pulls it itself via
// the search_codebase tool, as many times as it needs.
func buildMessages(history []chatMessage, question string) []anthropic.BetaMessageParam {
	messages := make([]anthropic.BetaMessageParam, 0, len(history)+1)

	for _, m := range history {
		block := anthropic.NewBetaTextBlock(m.Content)
		if m.Role == "assistant" {
			messages = append(messages, anthropic.BetaMessageParam{
				Role:    anthropic.BetaMessageParamRoleAssistant,
				Content: []anthropic.BetaContentBlockParamUnion{block},
			})
		} else {
			messages = append(messages, anthropic.NewBetaUserMessage(block))
		}
	}

	messages = append(messages, anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(question)))

	return messages
}

// extractText pulls the first text block out of the response content.
func extractText(msg *anthropic.BetaMessage) string {
	for _, block := range msg.Content {
		if block.Type == "text" {
			return block.Text
		}
	}
	return ""
}
