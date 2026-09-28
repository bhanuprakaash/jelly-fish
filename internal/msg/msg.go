// Package msg defines the neutral message format Providers translate to and
// from at the edge (ADR 0002). This slice only ever produces a text Part.
package msg

// CurrentVersion is the msg_v written into every Message.
const CurrentVersion = 1

// Roles a Message can carry.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// PartText is the only Part kind this slice produces.
const PartText = "text"

// Message is one provider-neutral chat message.
type Message struct {
	MsgV  int    `json:"msg_v"`
	Role  string `json:"role"`
	Parts []Part `json:"parts"`
}

// Part is one piece of a Message.
type Part struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// UserText builds a single-part text Message from the user.
func UserText(text string) Message {
	return Message{MsgV: CurrentVersion, Role: RoleUser, Parts: []Part{{Type: PartText, Text: text}}}
}
