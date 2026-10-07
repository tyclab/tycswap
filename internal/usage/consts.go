package usage

// SchemaVersion is the on-disk usage.json schema tag (04§2.2). A missing,
// corrupt, or mismatching version reads as an empty table.
const SchemaVersion = 2

const (
	StaleOKS            = 300.0
	ClaimTTLS           = 10.0
	TrustMaxAgeS        = 3600.0
	BackoffBaseS        = 30.0
	BackoffCapS         = 600.0
	RetryAfterFloorCapS = 900.0
	AuthDeadStrikes     = 1
)

// permanentAuthErrors are the fetch errors that prove the stored credential is
// permanently unusable and advance the dead-token strike count (04§2.2:
// PERMANENT_AUTH_ERRORS).
var permanentAuthErrors = map[string]bool{"invalid_grant": true}

const (
	ServeTTLS                 = 180.0
	MinIntervalS              = 180.0
	UrgentIntervalS           = 60.0
	ActiveMaxIntervalS        = 300.0
	CandidateDefaultIntervalS = 300.0
	CandidateMaxIntervalS     = 600.0
	MovementDeltaPct          = 1.0
	JitterFrac                = 0.1
	EdgeBackoffS              = 300.0
	Post429MinIntervalS       = 360.0
	Recent429WindowS          = 3600.0
	EscalationMarginPct       = 15.0
	// ResetSlackS: never schedule past a window reset + this.
	ResetSlackS = 60.0
	ParkCapS    = TrustMaxAgeS - ServeTTLS
)
