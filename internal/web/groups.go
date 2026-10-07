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
}

type GroupFacade interface {
	Views() []GroupView
	Switch(group, account string) (map[string]any, error)
}

func (s *Server) handleGroupSwitch(w http.ResponseWriter, r *http.Request) {
	if unavailable(w, s.d.Groups != nil, "session groups") {
		return
	}
	s.mutate(w, func() (map[string]any, error) {
		return s.d.Groups.Switch(r.PathValue("group"), r.PathValue("id"))
	})
}
