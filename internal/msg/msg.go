// Package msg defines the neutral message format Providers translate to and
// from at the edge (ADR 0002). This slice only ever produces a text Part.
package msg

import (
	"errors"
	"fmt"
	"strings"
)

// ErrUnsupported is returned for a Message this binary cannot read: a msg_v
// newer than CurrentVersion, or a Part kind it doesn't know (ADR 0002).
var ErrUnsupported = errors.New("unsupported message format")

// CurrentVersion is the msg_v written into every Message.
const CurrentVersion = 1

// Roles this slice produces.
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

// AssistantText builds a single-part text Message from the assistant.
func AssistantText(text string) Message {
	return Message{MsgV: CurrentVersion, Role: RoleAssistant, Parts: []Part{{Type: PartText, Text: text}}}
}

// Validate returns ErrUnsupported if m was written by a newer binary than
// this one.
func (m Message) Validate() error {
	if m.MsgV > CurrentVersion {
		return fmt.Errorf("msg_v %d: %w", m.MsgV, ErrUnsupported)
	}
	for _, p := range m.Parts {
		if p.Type != PartText {
			return fmt.Errorf("part type %q: %w", p.Type, ErrUnsupported)
		}
	}
	return nil
}

// Text joins the text of m's text Parts.
func (m Message) Text() string {
	var b strings.Builder
	for _, p := range m.Parts {
		if p.Type == PartText {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}
