package web

import (
	"net/http"

	"github.com/tyclab/tycswap/internal/brand"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/settings"
)

// SaveSetting writes a shared setting; nil resets. Session groups and their
// scoped settings are retired (DESIGN A78); a key only `tycswap config` sets
// is refused (DESIGN A93).
func (s *Server) SaveSetting(scope, key string, value *string) (map[string]any, error) {
	if scope != "" {
		return nil, cerr.Validation("session groups are retired; settings have one scope")
	}
	if configOnly(key) {
		name := brand.Sanitized().Name
		return nil, cerr.Validation("%s is set with %s config only: %s config set %s <value>, or %s config unset %s", key, name, name, key, name, key)
	}
	result, sequence, err := s.runMutation(func() (map[string]any, error) {
		result := map[string]any{"key": key}
		if s.d.Settings == nil {
			return nil, cerr.Validation("Default settings are unavailable")
		}
		if value == nil {
			removed, err := s.d.Settings.Unset(key)
			if err != nil {
				return nil, err
			}
			result["removed"] = removed
		} else {
			written, err := s.d.Settings.Set(key, *value)
			if err != nil {
				return nil, err
			}
			result["value"] = written
		}
		return result, nil
	})
	if err != nil {
		return nil, err
	}
	result["stateSequence"] = sequence
	return result, nil
}

func (s *Server) handleSettingSave(w http.ResponseWriter, r *http.Request, scope string, reset bool) {
	var value *string
	if !reset {
		var body struct {
			Value any `json:"value"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if body.Value == nil {
			writeError(w, http.StatusBadRequest, "value is required")
			return
		}
		raw := valueString(body.Value)
		value = &raw
	}
	key := r.PathValue("key")
	result, err := s.SaveSetting(scope, key, value)
	if err != nil {
		s.d.Logger("web: " + err.Error())
		writeError(w, statusFor(err), err.Error())
		return
	}
	if removed, _ := result["removed"].(bool); removed {
		s.logAction(r, "Unset "+key)
	} else if written, ok := result["value"]; ok {
		s.logAction(r, "Set "+key+"="+settings.FormatSettingValue(written))
	}
	sequence := result["stateSequence"]
	delete(result, "stateSequence")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result, "stateSequence": sequence})
}
