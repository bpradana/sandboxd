package model

import "time"

// Session is a long-lived shell inside a sandbox whose state (cwd, exported
// variables) carries across commands.
type Session struct {
	ID             string
	SandboxID      string
	Shell          string
	User           string
	State          string
	CreatedAt      time.Time
	LastActivityAt time.Time
}

func (s *Session) Clone() *Session {
	if s == nil {
		return nil
	}
	clone := *s
	return &clone
}
