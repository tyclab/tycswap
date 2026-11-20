package cli

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"strings"
	"testing"
)

// TestMain runs this as a stand-in for a one-question inbox bridge.
func fakePlacementBroker(in io.Reader, out io.Writer, decide func(string) string) int {
	var request struct {
		Question string
		Options  []string
	}
	if json.NewDecoder(in).Decode(&request) != nil || request.Question == "" ||
		strings.Join(request.Options, "|") != "Wait|Use this time" {
		return 1
	}
	answer := decide(request.Question)
	result := map[string]string{"status": "answered", "question_id": "fixture", "answer": answer}
	if answer == "expired" {
		result = map[string]string{"status": "unanswered"}
	}
	if json.NewEncoder(out).Encode(result) != nil {
		return 1
	}
	return 0
}

func TestPlacementQuestionBroker(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(childRoleEnv, "placement-broker")
	t.Setenv("TYCSWAP_PLACEMENT_BROKER", exe)
	for _, tc := range []struct {
		answer         string
		allow, failure bool
	}{
		{"Use this time", true, false},
		{"Wait", false, false},
		{"free text", false, true},
		{"expired", false, true},
	} {
		t.Run(tc.answer, func(t *testing.T) {
			t.Setenv("TEST_PLACEMENT_REPLY", tc.answer)
			ok, err := askPlacement(ioStreams{err: io.Discard}, "Use the synthetic account for this placement?", false)
			if ok != tc.allow || (err != nil) != tc.failure {
				t.Fatalf("approval=%v, err=%v", ok, err)
			}
		})
	}
}

func TestConcurrentPlacementQuestions(t *testing.T) {
	type outcome struct {
		question string
		granted  bool
		err      error
	}
	asked := make(chan string, 2)
	result := make(chan outcome, 2)
	decisions := map[string]chan string{"start A": make(chan string, 1), "start B": make(chan string, 1)}
	for question := range decisions {
		client, broker := net.Pipe()
		go func() {
			defer broker.Close()
			fakePlacementBroker(broker, broker, func(q string) string {
				asked <- q
				return <-decisions[q]
			})
		}()
		go func() {
			defer client.Close()
			approved, err := placementExchange(client, client, question)
			result <- outcome{question, approved, err}
		}()
	}
	<-asked
	<-asked
	decisions["start B"] <- placementUse
	if got := <-result; got.err != nil || got.question != "start B" || !got.granted {
		t.Fatalf("second start received %+v", got)
	}
	decisions["start A"] <- "Wait"
	if got := <-result; got.err != nil || got.question != "start A" || got.granted {
		t.Fatalf("first start received %+v", got)
	}
}
