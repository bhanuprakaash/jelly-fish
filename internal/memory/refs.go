package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/msg"
)

// Memory text never enters the Event Log (memory.md D28). A write call's text
// args are swapped for a placeholder before the reply is stored, and its
// results are stored as msg.MemoryRefs. Resolve puts the text back, from
// memory_revisions, when a prompt is built.

const (
	placeholder = "(memory text not stored)"
	deleted     = "(this memory was deleted)"
)

// textFields are the args of a memory call that hold memory text.
func textFields() []string { return []string{"title", "content", "old_str", "new_str"} }

// Redact returns args with the memory text swapped for a placeholder, and
// whether any was.
func Redact(raw json.RawMessage) (json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return json.RawMessage(`{}`), true
	}
	changed := false
	for _, f := range textFields() {
		if _, ok := m[f]; ok {
			m[f], changed = placeholderJSON(), true
		}
	}
	if !changed {
		return raw, false
	}
	out, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage(`{}`), true
	}
	return out, true
}

func placeholderJSON() json.RawMessage {
	b, _ := json.Marshal(placeholder)
	return b
}

// Lost reports whether args were redacted, so the call can't run as stored.
func Lost(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	for _, f := range textFields() {
		if bytes.Equal(m[f], placeholderJSON()) {
			return true
		}
	}
	return false
}

type revision struct {
	scope, path, title, content, project string
}

type call struct {
	command, path string
	ref           *msg.MemoryRef
}

// Resolve returns msgs with every memory ref and redacted arg replaced by
// text from memory_revisions. A ref whose memory was hard-deleted renders as
// "(this memory was deleted)", and drops out of an index. msgs is not changed.
func Resolve(ctx context.Context, pool *pgxpool.Pool, msgs []msg.Message) ([]msg.Message, error) {
	calls := map[string]*call{}
	var ids []uuid.UUID
	var versions []int
	for _, m := range msgs {
		for _, p := range m.Parts {
			switch {
			case p.Kind == msg.KindToolUse && p.ToolUse.Name == ToolName:
				var a args
				_ = json.Unmarshal(p.ToolUse.Args, &a)
				calls[p.ToolUse.ID] = &call{command: a.Command, path: a.Path}
			case p.Kind == msg.KindToolResult:
				for _, sp := range p.ToolResult.Parts {
					if sp.Kind != msg.KindMemoryRef {
						continue
					}
					id, err := uuid.Parse(sp.MemoryRef.MemoryID)
					if err != nil {
						return nil, fmt.Errorf("memory ref %q: %w", sp.MemoryRef.MemoryID, err)
					}
					ids, versions = append(ids, id), append(versions, sp.MemoryRef.Version)
					if c := calls[p.ToolResult.CallID]; c != nil && c.ref == nil {
						c.ref = sp.MemoryRef
					}
				}
			}
		}
	}
	if len(ids) == 0 {
		return msgs, nil
	}
	revs, err := loadRevisions(ctx, pool, ids, versions)
	if err != nil {
		return nil, err
	}

	out := make([]msg.Message, len(msgs))
	for i, m := range msgs {
		parts := make([]msg.Part, len(m.Parts))
		for j, p := range m.Parts {
			switch {
			case p.Kind == msg.KindToolUse && p.ToolUse.Name == ToolName:
				p = restoreArgs(p, calls[p.ToolUse.ID], revs)
			case p.Kind == msg.KindToolResult:
				p = resolveResult(p, calls[p.ToolResult.CallID], revs)
			}
			parts[j] = p
		}
		m.Parts = parts
		out[i] = m
	}
	return out, nil
}

func loadRevisions(ctx context.Context, pool *pgxpool.Pool, ids []uuid.UUID, versions []int) (map[msg.MemoryRef]revision, error) {
	rows, err := pool.Query(ctx, `
		SELECT r.memory_id, r.version, m.scope, r.path, r.title, r.content, coalesce(p.name, '')
		FROM unnest($1::uuid[], $2::int[]) AS k(id, v)
		JOIN memory_revisions r ON r.memory_id = k.id AND r.version = k.v
		JOIN memories m ON m.id = r.memory_id
		LEFT JOIN projects p ON p.id = m.project_id`, ids, versions)
	if err != nil {
		return nil, fmt.Errorf("load memory revisions: %w", err)
	}
	defer rows.Close()
	revs := map[msg.MemoryRef]revision{}
	for rows.Next() {
		var id uuid.UUID
		var version int
		var r revision
		if err := rows.Scan(&id, &version, &r.scope, &r.path, &r.title, &r.content, &r.project); err != nil {
			return nil, fmt.Errorf("scan memory revision: %w", err)
		}
		revs[msg.MemoryRef{MemoryID: id.String(), Version: version}] = r
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load memory revisions: %w", err)
	}
	return revs, nil
}

// restoreArgs puts a create call's title and content back from the revision
// the call wrote. The other commands' text can't be rebuilt from a revision,
// so their placeholders stay.
func restoreArgs(p msg.Part, c *call, revs map[msg.MemoryRef]revision) msg.Part {
	if c == nil || c.command != cmdCreate || c.ref == nil {
		return p
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(p.ToolUse.Args, &m) != nil {
		return p
	}
	rev, ok := revs[*c.ref]
	for f, text := range map[string]string{"title": rev.title, "content": rev.content} {
		if !bytes.Equal(m[f], placeholderJSON()) {
			continue
		}
		if !ok {
			text = deleted
		}
		m[f], _ = json.Marshal(text)
	}
	args, err := json.Marshal(m)
	if err != nil {
		return p
	}
	use := *p.ToolUse
	use.Args = args
	p.ToolUse = &use
	return p
}

// resolveResult renders the refs of a memory result: a viewed memory as its
// content, the index as lines, a str_replace or insert's own ref as the text it
// left, and any other write's own ref as nothing.
func resolveResult(p msg.Part, c *call, revs map[msg.MemoryRef]revision) msg.Part {
	var parts []msg.Part
	var lines []revision
	for _, sp := range p.ToolResult.Parts {
		if sp.Kind != msg.KindMemoryRef {
			parts = append(parts, sp)
			continue
		}
		rev, ok := revs[*sp.MemoryRef]
		switch {
		case c == nil:
		case c.command == cmdStrReplace || c.command == cmdInsert:
			text := deleted
			if ok {
				text = "It now reads:\n" + rev.content
			}
			parts = append(parts, msg.Part{Kind: msg.KindText, Text: text})
		case c.command != cmdView:
		case c.path == rootPath:
			if ok {
				lines = append(lines, rev)
			}
		case ok:
			parts = append(parts, msg.Part{Kind: msg.KindText, Text: rev.content})
		default:
			parts = append(parts, msg.Part{Kind: msg.KindText, Text: deleted})
		}
	}
	if c != nil && c.command == cmdView && c.path == rootPath {
		text := renderBlocks(lines)
		if text == "" {
			text = "(no memories)"
		}
		parts = append(parts, msg.Part{Kind: msg.KindText, Text: text})
	}
	tr := *p.ToolResult
	tr.Parts = parts
	p.ToolResult = &tr
	return p
}
