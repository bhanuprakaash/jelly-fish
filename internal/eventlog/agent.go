package eventlog

import "github.com/google/uuid"

// generalAgent is the hard-coded "General" Agent every session uses until
// Agents exist (agents-skills.md D14).
func generalAgent() uuid.UUID { return uuid.MustParse("00000000-0000-0000-0000-000000000004") }
