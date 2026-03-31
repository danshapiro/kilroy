package codexappserver

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type jsonRPCMessage struct {
	JSONRPC string        `json:"jsonrpc,omitempty"`
	ID      any           `json:"id,omitempty"`
	Method  string        `json:"method,omitempty"`
	Params  any           `json:"params,omitempty"`
	Result  any           `json:"result,omitempty"`
	Error   *jsonRPCError `json:"error,omitempty"`
}

type modelReasoningEffort struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

type modelEntry struct {
	ID                     string                 `json:"id"`
	Model                  string                 `json:"model"`
	DisplayName            string                 `json:"displayName"`
	Hidden                 bool                   `json:"hidden"`
	IsDefault              bool                   `json:"isDefault"`
	DefaultReasoningEffort string                 `json:"defaultReasoningEffort"`
	ReasoningEffort        []modelReasoningEffort `json:"reasoningEffort"`
	InputModalities        []string               `json:"inputModalities"`
	SupportsPersonality    bool                   `json:"supportsPersonality"`
	Upgrade                any                    `json:"upgrade,omitempty"`
}

type modelListResponse struct {
	Data       []modelEntry `json:"data"`
	NextCursor any          `json:"nextCursor"`
}
