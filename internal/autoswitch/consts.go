// Package autoswitch is the UI-agnostic threshold-policy auto-switch engine.
package autoswitch

const (
	StateFilename      = "autoswitch_state.json"
	StateSchemaVersion = 1
)

const (
	// Twice Claude Code's own 5-minute refresh buffer.
	FreshenBufferMS  = 10 * 60 * 1000
	MaxSleepS        = 6 * 3600.0
	NoResetFallbackS = 300.0
	IdleHoldMaxS     = 30 * 60.0
)
