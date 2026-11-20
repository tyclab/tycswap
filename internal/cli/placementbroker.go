package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
)

const placementUse = "Use this time"

// Only the start requesting a placement owns this connection. Closing stdin
// cancels its outstanding question in the broker; no store lock is held.
func askPlacementBroker(path, question, detail string) (bool, error) {
	if !filepath.IsAbs(path) {
		return false, errors.New("TYCSWAP_PLACEMENT_BROKER must name an absolute executable path")
	}
	cmd := exec.Command(path, "--ask")
	in, err := cmd.StdinPipe()
	if err != nil {
		return false, err
	}
	defer in.Close()
	out, err := cmd.StdoutPipe()
	if err != nil {
		return false, err
	}
	if err := cmd.Start(); err != nil {
		return false, errors.New("operator decision needed: could not start the placement broker")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	stopClose := context.AfterFunc(ctx, func() { in.Close() })
	answer, err := placementExchange(in, out, question, detail)
	stopClose()
	in.Close()
	if waitErr := cmd.Wait(); err == nil && waitErr != nil {
		err = errors.New("placement broker did not finish successfully")
	}
	if err != nil {
		return false, fmt.Errorf("operator decision needed: %w", err)
	}
	if ctx.Err() != nil {
		return false, errors.New("placement question cancelled")
	}
	return answer, nil
}

// The broker accepts one JSON question and sends its final decision. An open
// input pipe means the requesting process still wants the answer.
func placementExchange(in io.Writer, out io.Reader, question, detail string) (bool, error) {
	request := struct {
		Question string   `json:"question"`
		Context  string   `json:"context,omitempty"`
		Options  []string `json:"options"`
	}{question, detail, []string{"Wait", placementUse}}
	if err := json.NewEncoder(in).Encode(request); err != nil {
		return false, errors.New("could not send the placement question")
	}
	var answer struct {
		Status, Answer string
		QuestionID     string `json:"question_id"`
	}
	if err := json.NewDecoder(out).Decode(&answer); err != nil || answer.Status != "answered" || answer.QuestionID == "" {
		return false, errors.New("placement question expired, was cancelled or is unavailable")
	}
	switch answer.Answer {
	case placementUse:
		return true, nil
	case "Wait":
		return false, nil
	default:
		return false, errors.New("unrecognized placement answer; retry the start to decide")
	}
}
