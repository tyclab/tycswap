// accounts_test.go — the /backend-api/accounts request and its parsing (the
// fetch_workspace_names cases of claude-swap PR #252 tests/test_codex_usage.py;
// the grouped-scope rules of test_codex_workspaces.py belong to a later wave).

package api

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestFetchAccounts(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		want    []Workspace
		wantErr bool
	}{
		{"names are keyed by account id", 200, `{"items":[{"id":"team-1","name":"Workspace Alpha"}]}`,
			[]Workspace{{AccountID: "team-1", Name: "Workspace Alpha"}}, false},
		// Storing "" would look like a real answer and stop a later successful
		// fetch from filling the name in.
		{"null and empty names are omitted, not stored as empty", 200, `{"items":[{"id":"team-1","name":null},{"id":"team-2","name":""}]}`,
			[]Workspace{}, false},
		{"rows without a usable id or object shape are skipped", 200, `{"items":["x",{"name":"No Id"},{"id":"","name":"Empty"},{"id":7,"name":"Numeric"}]}`,
			[]Workspace{{AccountID: "7", Name: "Numeric"}}, false},
		{"a plan_type is normalized when present", 200, `{"items":[{"id":"team-1","name":"A","plan_type":"team"}]}`,
			[]Workspace{{AccountID: "team-1", Name: "A", Plan: "business"}}, false},
		{"a duplicated id takes the last name", 200, `{"items":[{"id":"t","name":"Old"},{"id":"u","name":"U"},{"id":"t","name":"New"}]}`,
			[]Workspace{{AccountID: "t", Name: "New"}, {AccountID: "u", Name: "U"}}, false},
		{"no items list is a failure", 200, `{"accounts":[]}`, nil, true},
		{"a non-object body is a failure", 200, `[1]`, nil, true},
		{"an unparseable body is a failure", 200, `junk`, nil, true},
		{"an http error is a failure", 403, `{}`, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(t, tc.status, tc.body, nil)
			got, err := clientFor(s).FetchAccounts(context.Background(), "at", "acct")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("workspaces = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestFetchAccounts_SendsBothRequiredHeaders(t *testing.T) {
	s := newServer(t, 200, `{"items":[]}`, nil)
	clientFor(s).FetchAccounts(context.Background(), "at-1", "acct-1")
	req := s.last(t)
	if req.Method != "GET" || req.Path != "/backend-api/accounts" {
		t.Errorf("request = %s %s", req.Method, req.Path)
	}
	if req.Header.Get("Authorization") != "Bearer at-1" || req.Header.Get("ChatGPT-Account-Id") != "acct-1" {
		t.Errorf("headers = %v", req.Header)
	}
	if NewHTTPClient().AccountsURL != AccountsURL {
		t.Errorf("production AccountsURL = %q", NewHTTPClient().AccountsURL)
	}
}

// A missing workspace name is cosmetic: every failure is a plain error the
// caller answers by leaving stored names untouched.
func TestFetchAccounts_AFailureIsNeverFatal(t *testing.T) {
	if ws, err := deadClient(t).FetchAccounts(context.Background(), "at", "acct"); err == nil || ws != nil {
		t.Errorf("got %v, %v; want nil and an error", ws, err)
	}
}

func TestFetchAccounts_MissingAuthMakesNoRequest(t *testing.T) {
	s := newServer(t, 200, `{"items":[]}`, nil)
	for _, in := range [][2]string{{"", "acct"}, {"at", ""}} {
		if _, err := clientFor(s).FetchAccounts(context.Background(), in[0], in[1]); !errors.Is(err, ErrMissingAuth) {
			t.Errorf("FetchAccounts(%q, %q) err = %v, want ErrMissingAuth", in[0], in[1], err)
		}
	}
	if s.hits() != 0 {
		t.Errorf("made %d requests", s.hits())
	}
}
