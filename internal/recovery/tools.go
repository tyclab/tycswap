package recovery

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
)

const MaxToolTranscriptBytes = 256 * 1024 * 1024

type ToolState struct {
	Known      bool
	Pending    int
	Background bool
}

func InspectTools(path, provider string) (ToolState, error) {
	f, err := os.Open(path)
	if err != nil {
		return ToolState{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ToolState{}, err
	}
	if info.Size() > MaxToolTranscriptBytes {
		return ToolState{}, nil
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), MaxTranscriptLine)
	pending := map[string]bool{}
	state := ToolState{Known: true}
	for scanner.Scan() {
		var row struct {
			Type    string          `json:"type"`
			Message json.RawMessage `json:"message"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &row) != nil {
			state.Known = false
			continue
		}
		if provider == "claude" {
			var message struct {
				Content []struct {
					Type   string `json:"type"`
					ID     string `json:"id"`
					ToolID string `json:"tool_use_id"`
					Input  struct {
						Background bool `json:"run_in_background"`
					} `json:"input"`
				} `json:"content"`
			}
			if len(row.Message) == 0 {
				continue
			}
			if json.Unmarshal(row.Message, &message) != nil {
				var plain struct {
					Content string `json:"content"`
				}
				if json.Unmarshal(row.Message, &plain) != nil {
					state.Known = false
				}
				continue
			}
			for _, block := range message.Content {
				switch block.Type {
				case "tool_use":
					if block.ID != "" {
						pending[block.ID] = true
					} else {
						state.Known = false
					}
					if block.Input.Background {
						state.Background = true
					}
				case "tool_result":
					if block.ToolID == "" {
						state.Known = false
					}
					delete(pending, block.ToolID)
				}
			}
		} else if provider == "codex" {
			if row.Type != "response_item" {
				continue
			}
			var item struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
			}
			if json.Unmarshal(row.Payload, &item) != nil {
				state.Known = false
				continue
			}
			switch item.Type {
			case "function_call", "custom_tool_call":
				if item.CallID != "" {
					pending[item.CallID] = true
				} else {
					state.Known = false
				}
			case "function_call_output", "custom_tool_call_output":
				if item.CallID == "" {
					state.Known = false
				}
				delete(pending, item.CallID)
			}
		} else {
			return ToolState{}, errors.New("unsupported transcript provider")
		}
	}
	if err := scanner.Err(); err != nil {
		state.Known = false
	}
	state.Pending = len(pending)
	return state, nil
}
