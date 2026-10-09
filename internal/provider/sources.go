package provider

import (
	"fmt"
	"slices"
	"strings"

	"github.com/bhanuprakaash/jelly-fish/internal/msg"
)

// ForeignSources rewrites messages for the Provider named own. A reply with
// a Native part own didn't write was grounded by another Provider's search,
// which own can't read, so its cited pages go at the end of its last text
// part as plain text (provider-gateway.md D6a).
func ForeignSources(messages []msg.Message, own string) []msg.Message {
	out := slices.Clone(messages)
	for i, m := range out {
		if !slices.ContainsFunc(m.Parts, func(p msg.Part) bool { return p.Kind == msg.KindNative && p.Native.Provider != own }) {
			continue
		}
		parts := slices.Clone(m.Parts)
		var list strings.Builder
		seen := map[string]bool{}
		last := -1
		for j, p := range parts {
			if p.Kind != msg.KindText {
				continue
			}
			last = j
			for _, c := range p.Citations {
				if seen[c.URL] {
					continue
				}
				seen[c.URL] = true
				if c.Title == "" {
					fmt.Fprintf(&list, "\n- %s", c.URL)
				} else {
					fmt.Fprintf(&list, "\n- %s (%s)", c.Title, c.URL)
				}
			}
		}
		if last >= 0 && list.Len() > 0 {
			parts[last].Text += "\n\nSources:" + list.String()
		}
		out[i].Parts = parts
	}
	return out
}
