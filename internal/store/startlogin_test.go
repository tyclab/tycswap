package store

import (
	"encoding/json"
	"testing"
)

func TestStartLoginFromOnlyAcceptsBooleanTrue(t *testing.T) {
	for _, value := range []string{"true", "false", "null", `"true"`, "1", "[]", "{}"} {
		data := &SequenceData{Accounts: map[string]json.RawMessage{"1": json.RawMessage(`{"startLogin":` + value + `}`)}}
		if got := StartLoginFrom(data, "1"); got != (value == "true") {
			t.Fatalf("%s = %v", value, got)
		}
	}
	if StartLoginFrom(nil, "1") || StartLoginFrom(&SequenceData{}, "1") {
		t.Fatal("absent record marked")
	}
}
