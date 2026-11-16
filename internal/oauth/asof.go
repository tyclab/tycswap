package oauth

import "time"

// AsOf reads a normalized usage map at now: a window whose reset time has
// passed reads 0 %, because its cached reading predates the renewal (DESIGN
// A91, ENG-7). The engine and the default-session policy read windows through
// it, so they agree on which account has room. The map comes back as it is
// when no window renewed, else as a copy; normalized is never changed.
func AsOf(normalized map[string]any, now time.Time) map[string]any {
	var out map[string]any
	set := func(key string, v any) {
		if out == nil {
			out = make(map[string]any, len(normalized))
			for k, val := range normalized {
				out[k] = val
			}
		}
		out[key] = v
	}
	for _, key := range []string{"five_hour", "seven_day"} {
		if w, renewed := renewedWindow(normalized[key], now); renewed {
			set(key, w)
		}
	}
	if scoped, ok := normalized["scoped"].([]any); ok {
		var list []any
		for i, item := range scoped {
			if w, renewed := renewedWindow(item, now); renewed {
				if list == nil {
					list = append([]any(nil), scoped...)
				}
				list[i] = w
			}
		}
		if list != nil {
			set("scoped", list)
		}
	}
	if out == nil {
		return normalized
	}
	return out
}

// renewedWindow is a copy of a window at 0 % when its reset has passed.
func renewedWindow(v any, now time.Time) (map[string]any, bool) {
	w, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	pct, ok := pctFloat(w["pct"])
	resetsAt, _ := w["resets_at"].(string)
	reset, parsed := parseResetTime(resetsAt)
	if !ok || pct == 0 || !parsed || reset.After(now) {
		return nil, false
	}
	c := make(map[string]any, len(w))
	for k, val := range w {
		c[k] = val
	}
	c["pct"] = 0.0
	return c, true
}
