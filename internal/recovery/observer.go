package recovery

import (
	"bufio"
	"encoding/json"
	"io"
	"time"
)

// ObserveCodex consumes a copy of supported app-server notifications, never CLI display text.
func (s *Store) ObserveCodex(reader io.Reader, model string, onEvent func(Event)) error {
	return s.ObserveCodexBound(reader, model, "", "", onEvent)
}

func (s *Store) ObserveCodexBound(reader io.Reader, model, accountID, identity string, onEvent func(Event)) error {
	return s.ObserveCodexOwned(reader, model, accountID, identity, 0, onEvent)
}

func (s *Store) ObserveCodexOwned(reader io.Reader, model, accountID, identity string, sourcePID int, onEvent func(Event)) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), MaxEventBytes)
	for scanner.Scan() {
		var header struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
			return err
		}
		switch header.Method {
		case "thread/started", "turn/started", "turn/completed", "error":
		default:
			continue
		}
		event, err := ParseCodex(scanner.Bytes(), model, time.Now())
		if err != nil {
			return err
		}
		if event.Kind == "turn/started" || event.Kind == "thread/started" {
			event.AccountID = accountID
			event.AccountIdentity = identity
			event.SourcePID = sourcePID
		}
		event, err = s.Record(event)
		if err != nil {
			return err
		}
		if onEvent != nil {
			onEvent(event)
		}
	}
	return scanner.Err()
}
