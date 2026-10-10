package eventlog

import (
	"strings"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/msg"
)

// Budget is the token, dollar and turn limits of a session; a zero limit is
// no limit. Turns count per user.message (event-log.md §5.9).
type Budget struct {
	Tokens     int64 `json:"tokens"`
	CostMicros int64 `json:"cost_micros"`
	Turns      int   `json:"turns"`
}

// Budget dimensions, as budget.exceeded and approval.requested name them.
const (
	DimTokens  = "tokens"
	DimDollars = "dollars"
	DimTurns   = "turns"
)

// ApprovalKindBudget is the kind of an approval.requested for a Budget limit.
const ApprovalKindBudget = "budget"

// DecisionAllow is the approval.resolved decision that raises a Budget limit.
const DecisionAllow = "allow"

func generalBudget() Budget {
	return Budget{Tokens: 500_000, CostMicros: 2_000_000, Turns: 25}
}

func sessionCreatedPayload(model string) map[string]any {
	return map[string]any{
		"agent_id": generalAgent(),
		"agent":    map[string]any{"name": "General", "model": model, "budget": generalBudget()},
		"trigger":  TriggerUserMessage,
	}
}

func userMessagePayload(clientMsgID uuid.UUID, text string) map[string]any {
	return map[string]any{
		"client_msg_id": clientMsgID,
		"message":       msg.UserText(text),
	}
}

// UsageRecorded is the payload of a usage.recorded event (event-log.md §4).
type UsageRecorded struct {
	Kind       string `json:"kind"`
	Provider   string `json:"provider"`
	Model      string `json:"model,omitempty"`
	Quantity   int64  `json:"quantity"`
	Unit       string `json:"unit"`
	CostMicros int64  `json:"cost_micros"`
}

// KindLLM is the usage kind that feeds the session's budget counters.
const KindLLM = "llm"

// UnitWebSearchRequests is the usage unit of Provider built-in searches. It
// counts searches, not tokens, so it adds to the cost counter only.
const UnitWebSearchRequests = "web_search_requests"

// SessionRenamed is the payload of a session.renamed event (event-log.md §4,
// D44).
type SessionRenamed struct {
	Title string `json:"title"`
	By    string `json:"by"`
}

// Who set a Title.
const (
	RenamedByUser = "user"
	RenamedByAuto = "auto"
)

// MaxTitleRunes is the longest Title, in characters.
const MaxTitleRunes = 100

const titleQuotes = "\"'“”‘’"

// CleanTitle tidies a model's Title reply: surrounding whitespace and one
// pair of quotes go, and it is cut to MaxTitleRunes (event-log.md D45).
func CleanTitle(raw string) string {
	r := []rune(strings.TrimSpace(raw))
	if n := len(r); n >= 2 && strings.ContainsRune(titleQuotes, r[0]) && strings.ContainsRune(titleQuotes, r[n-1]) {
		r = []rune(strings.TrimSpace(string(r[1 : n-1])))
	}
	return strings.TrimSpace(string(r[:min(len(r), MaxTitleRunes)]))
}
