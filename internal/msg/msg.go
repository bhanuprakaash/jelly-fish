// Package msg defines the neutral message format Providers translate to and
// from at the edge (ADR 0002, provider-gateway.md §3.1).
package msg

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrUnsupported is returned for a Message this binary cannot read: a msg_v
// newer than CurrentVersion, or a Part kind it doesn't know (ADR 0002).
var ErrUnsupported = errors.New("unsupported message format")

// CurrentVersion is the msg_v written into every Message. A new Kind never
// bumps it; a changed shape of an existing Kind does, with an upcaster
// (provider-gateway.md §3.2).
const CurrentVersion = 2

// Roles a Message can have; system text lives in the request, not here.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Kind says which of a Part's fields hold its content.
type Kind string

// Part kinds this binary reads and writes.
const (
	KindText       Kind = "text"
	KindToolUse    Kind = "tool_use"
	KindToolResult Kind = "tool_result"
	KindThinking   Kind = "thinking"
	KindNative     Kind = "native"
	KindMemoryRef  Kind = "memory_ref"
)

// kindFields lists the Part fields each Kind sets. A Kind missing here is
// unknown and makes its Message unreadable.
func kindFields() map[Kind][]string {
	return map[Kind][]string{
		KindText:       {"Text"},
		KindToolUse:    {"ToolUse"},
		KindToolResult: {"ToolResult"},
		KindThinking:   {"Thinking"},
		KindNative:     {"Native"},
		KindMemoryRef:  {"MemoryRef"},
	}
}

// Message is one provider-neutral chat message.
type Message struct {
	MsgV  int    `json:"msg_v"`
	Role  string `json:"role"`
	Parts []Part `json:"parts"`
}

// Part is one piece of a Message. Keys are short so stored payloads stay
// small (provider-gateway.md §3.1 legend).
type Part struct {
	Kind       Kind        `json:"k"`
	Text       string      `json:"text,omitempty"`
	Citations  []Citation  `json:"cit,omitempty"`
	ToolUse    *ToolUse    `json:"tu,omitempty"`
	ToolResult *ToolResult `json:"tr,omitempty"`
	Thinking   *Thinking   `json:"th,omitempty"`
	Native     *Native     `json:"nat,omitempty"`
	MemoryRef  *MemoryRef  `json:"mem,omitempty"`
}

// Citation is a web page that a text Part's claims rest on.
type Citation struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
}

// ToolUse is a tool call the model asked for.
type ToolUse struct {
	// ID is ours, set even when the Provider gave none.
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
	// Opaque is the Provider's own call id, valid only to the Provider that
	// wrote it: an adapter replays it when Provider is its own.
	Opaque []byte `json:"opaque,omitempty"`
	// Provider names who wrote the call. The Worker fills it from the turn's
	// events; it is never stored.
	Provider string `json:"-"`
}

// ToolResult answers the ToolUse whose ID is CallID.
type ToolResult struct {
	CallID  string `json:"call_id"`
	Parts   []Part `json:"parts"`
	IsError bool   `json:"is_error,omitempty"`
}

// Thinking is the model's reasoning. Opaque is the Provider's signature,
// replayed exactly and only to the Provider that wrote it.
type Thinking struct {
	Text     string `json:"text,omitempty"`
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
	Opaque   []byte `json:"opaque,omitempty"`
}

// Native is a Provider block with no neutral equivalent, replayed only to
// Provider and dropped for any other.
type Native struct {
	Provider string          `json:"provider"`
	Type     string          `json:"type"`
	Raw      json.RawMessage `json:"raw"`
}

// MemoryRef points at one version of a memory, so a stored tool result names
// the memory without holding its text, which lives only in memory_revisions.
type MemoryRef struct {
	MemoryID string `json:"id"`
	Version  int    `json:"v"`
}

// UserText builds a single-part text Message from the user.
func UserText(text string) Message {
	return Message{MsgV: CurrentVersion, Role: RoleUser, Parts: []Part{{Kind: KindText, Text: text}}}
}

// AssistantText builds a single-part text Message from the assistant.
func AssistantText(text string) Message {
	return Message{MsgV: CurrentVersion, Role: RoleAssistant, Parts: []Part{{Kind: KindText, Text: text}}}
}

// UnmarshalJSON reads a stored Message of any msg_v up to CurrentVersion,
// upcasting older ones. It returns ErrUnsupported for a newer msg_v or an
// unknown Kind, so a Worker can release the Lease instead of guessing.
func (m *Message) UnmarshalJSON(b []byte) error {
	var head struct {
		MsgV int `json:"msg_v"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return err
	}
	type current Message
	switch {
	case head.MsgV > CurrentVersion:
		return fmt.Errorf("msg_v %d: %w", head.MsgV, ErrUnsupported)
	case head.MsgV <= 1:
		up, err := upcastV1(b)
		if err != nil {
			return err
		}
		*m = up
	default:
		if err := json.Unmarshal(b, (*current)(m)); err != nil {
			return err
		}
	}
	return checkKinds(m.Parts, kindFields())
}

// upcastV1 reads msg_v 1, whose parts were {"type":"text","text":…}.
func upcastV1(b []byte) (Message, error) {
	var v1 struct {
		Role  string `json:"role"`
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(b, &v1); err != nil {
		return Message{}, err
	}
	m := Message{MsgV: CurrentVersion, Role: v1.Role, Parts: make([]Part, len(v1.Parts))}
	for i, p := range v1.Parts {
		if p.Type != string(KindText) {
			return Message{}, fmt.Errorf("msg_v 1 part type %q: %w", p.Type, ErrUnsupported)
		}
		m.Parts[i] = Part{Kind: KindText, Text: p.Text}
	}
	return m, nil
}

func checkKinds(parts []Part, known map[Kind][]string) error {
	for _, p := range parts {
		if _, ok := known[p.Kind]; !ok {
			return fmt.Errorf("part kind %q: %w", p.Kind, ErrUnsupported)
		}
		if p.ToolResult != nil {
			if err := checkKinds(p.ToolResult.Parts, known); err != nil {
				return err
			}
		}
	}
	return nil
}

// Text joins the text of m's text Parts.
func (m Message) Text() string {
	var b strings.Builder
	for _, p := range m.Parts {
		if p.Kind == KindText {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// ToolResultText joins the text of the tool results in m with "; ", in
// order. It is empty when m holds none.
func (m Message) ToolResultText() string {
	var texts []string
	for _, p := range m.Parts {
		if p.Kind == KindToolResult {
			texts = append(texts, Message{Parts: p.ToolResult.Parts}.Text())
		}
	}
	return strings.Join(texts, "; ")
}
