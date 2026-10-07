package web

import "net/http"

type GroupView struct {
	ID              string            `json:"id"`
	Label           string            `json:"label"`
	ActiveNumber    string            `json:"activeNumber"`
	LiveSessions    int               `json:"liveSessions"`
	Blocker         string            `json:"blocker,omitempty"`
	Pending         bool              `json:"pending"`
	AccountBlockers map[string]string `json:"accountBlockers"`
	Auto            *AutoView         `json:"auto,omitempty"`
	Settings        map[string]any    `json:"settings,omitempty"`
	SettingViews    []SettingView     `json:"settingViews,omitempty"`
}

type GroupFacade interface {
	Views() []GroupView
	Switch(group, account string) (map[string]any, error)
}

type GroupSettingsFacade interface {
	Settings(group string) ([]SettingView, error)
	SetSetting(group, key, raw string) (any, error)
	UnsetSetting(group, key string) (bool, error)
}

func (s *Server) handleGroupSettings(w http.ResponseWriter, r *http.Request) {
	facade, ok := s.d.Groups.(GroupSettingsFacade)
	if unavailable(w, ok, "group settings") {
		return
	}
	views, err := facade.Settings(r.PathValue("group"))
	if err != nil {
		writeError(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": s.groupSettingViews(views)})
}

func (s *Server) groupSettingViews(views []SettingView) []SettingView {
	list := make([]SettingView, len(views))
	copy(list, views)
	managed := s.d.Auto != nil && s.d.Auto.View().ManagedBy != ""
	for i := range list {
		if list[i].Source != "default" || list[i].ReadOnly != "" {
			continue
		}
		list[i].Applies = "Inherited Default policy loads when a continuous rotation runner starts. Group overrides reload at each group rotation check."
		if managed {
			list[i].Applies = "Inherited from Default at the external scheduler's next group rotation check."
		}
	}
	return list
}

func (s *Server) handleGroupSettingSet(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.d.Groups.(GroupSettingsFacade); unavailable(w, ok, "group settings") {
		return
	}
	s.handleSettingSave(w, r, r.PathValue("group"), false)
}

func (s *Server) handleGroupSettingUnset(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.d.Groups.(GroupSettingsFacade); unavailable(w, ok, "group settings") {
		return
	}
	s.handleSettingSave(w, r, r.PathValue("group"), true)
}

func (s *Server) handleGroupSwitch(w http.ResponseWriter, r *http.Request) {
	if unavailable(w, s.d.Groups != nil, "session groups") {
		return
	}
	s.mutate(w, func() (map[string]any, error) {
		return s.d.Groups.Switch(r.PathValue("group"), r.PathValue("id"))
	})
}
