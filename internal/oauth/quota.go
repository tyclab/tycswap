package oauth

// HasSharedQuota requires a usable week and validates the optional session
// window. Ultra's staged rollout is reflected by reported windows, not its name.
func HasSharedQuota(normalized map[string]any) bool {
	u := NewUsage(normalized)
	if u == nil || u.SevenDay == nil || u.SevenDay.Pct < 0 {
		return false
	}
	if normalized["five_hour"] == nil {
		return true
	}
	return u.FiveHour != nil && u.FiveHour.Pct >= 0
}
