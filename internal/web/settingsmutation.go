package web

import (
	"net/http"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/groups"
)

// SaveSetting uses an empty scope for shared defaults; nil resets.
func (s *Server) SaveSetting(scope, key string, value *string) (map[string]any, error) {
	if scope != "" {
		id, err := groups.Parse(scope)
		if err != nil {
			return nil, cerr.Validation("%s", err)
		}
		scope = string(id)
	}
	result, sequence, err := s.runMutation(func() (map[string]any, error) {
		result := map[string]any{"key": key}
		if scope != "" {
			facade, ok := s.d.Groups.(GroupSettingsFacade)
			if !ok {
				return nil, cerr.Validation("group settings are unavailable")
			}
			result["scope"] = scope
			if value == nil {
				removed, err := facade.UnsetSetting(scope, key)
				result["removed"] = removed
				return result, err
			}
			written, err := facade.SetSetting(scope, key, *value)
			result["value"] = written
			return result, err
		}
		if s.d.Settings == nil {
			return nil, cerr.Validation("Default settings are unavailable")
		}
		model := ""
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
			model = *value
		}
		applied, err := s.applyModelSetting(key, model)
		if applied {
			result["applied"] = true
		}
		return result, err
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
	result, err := s.SaveSetting(scope, r.PathValue("key"), value)
	if err != nil {
		s.d.Logger("web: " + err.Error())
		writeError(w, statusFor(err), err.Error())
		return
	}
	sequence := result["stateSequence"]
	delete(result, "stateSequence")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result, "stateSequence": sequence})
}
