package agent

import "github.com/danshapiro/kilroy/internal/llm"

type TurnKind string

const (
	TurnUserInput TurnKind = "USER_INPUT"
	TurnSteering  TurnKind = "STEERING"
	TurnAssistant TurnKind = "ASSISTANT"
	TurnTool      TurnKind = "TOOL"
)

// Turn is the Session's typed history item. Steering turns are kept distinct for observability,
// but are converted to user-role messages when building the LLM request.
type Turn struct {
	Kind    TurnKind
	Message llm.Message
}
