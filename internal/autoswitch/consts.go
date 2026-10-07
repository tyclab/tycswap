package autoswitch

const (
	StateFilename      = "autoswitch_state.json"
	StateSchemaVersion = 1
)

const (
	FreshenBufferMS = 10 * 60 * 1000
	MaxSleepS       = 6 * 3600.0
	// NoResetFallbackS: blocked/idle-hold cadence when no reset time is known.
	NoResetFallbackS = 300.0
	IdleHoldMaxS     = 30 * 60.0
)
