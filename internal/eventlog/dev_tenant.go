package eventlog

import "github.com/google/uuid"

// Dev tenant ids, hard-coded until login (#3) exists (S1 slice decisions).
const (
	devWorkspaceID = "00000000-0000-0000-0000-000000000001"
	devProjectID   = "00000000-0000-0000-0000-000000000002"
	devUserID      = "00000000-0000-0000-0000-000000000003"
	devAgentID     = "00000000-0000-0000-0000-000000000004"
)

// DevScope is the hard-coded TenantScope every request uses until login (#3)
// replaces it with a real one.
func DevScope() TenantScope {
	return TenantScope{WorkspaceID: uuid.MustParse(devWorkspaceID), UserID: uuid.MustParse(devUserID)}
}

func devProject() uuid.UUID { return uuid.MustParse(devProjectID) }

func devAgent() uuid.UUID { return uuid.MustParse(devAgentID) }
