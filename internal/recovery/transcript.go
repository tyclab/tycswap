package recovery

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
)

const MaxTranscriptLine = 2 * 1024 * 1024
const MaxTranscriptBytes = 16 * 1024 * 1024

func ReadTranscript(path, provider string) ([]Message, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	omissions := []string{}
	if info.Size() > MaxTranscriptBytes {
		if _, err := f.Seek(info.Size()-MaxTranscriptBytes, io.SeekStart); err != nil {
			return nil, nil, err
		}
		reader := bufio.NewReader(f)
		if _, err := reader.ReadBytes('\n'); err != nil && err != io.EOF {
			return nil, nil, err
		}
		omissions = append(omissions, "Older transcript data omitted (bounded tail read)")
		return readLines(reader, provider, omissions)
	}
	return readLines(bufio.NewReader(f), provider, omissions)
}

func readLines(reader *bufio.Reader, provider string, omissions []string) ([]Message, []string, error) {
	messages := []Message{}
	for {
		line, err := reader.ReadSlice('\n')
		oversized := false
		if err == bufio.ErrBufferFull {
			accumulated := append([]byte{}, line...)
			for err == bufio.ErrBufferFull {
				line, err = reader.ReadSlice('\n')
				if len(accumulated)+len(line) > MaxTranscriptLine {
					oversized = true
				}
				if !oversized {
					accumulated = append(accumulated, line...)
				}
			}
			line = accumulated
		}
		if oversized {
			omissions = append(omissions, "Oversized transcript record omitted")
		} else if len(line) > 0 {
			if !json.Valid(line) {
				omissions = append(omissions, "Partial or malformed transcript record omitted")
			} else {
				msg, ok, omitted := transcriptMessage(line, provider)
				if omitted {
					omissions = append(omissions, "Attachments or non-text transcript blocks omitted")
				}
				if ok {
					messages = append(messages, msg)
					if len(messages) > MaxRecentMessages {
						messages = messages[1:]
						omissions = append(omissions, "Older messages omitted")
					}
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, unique(omissions), err
		}
	}
	return messages, unique(omissions), nil
}

func transcriptMessage(line []byte, provider string) (Message, bool, bool) {
	var envelope struct {
		Type    string          `json:"type"`
		Message json.RawMessage `json:"message"`
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(line, &envelope) != nil {
		return Message{}, false, false
	}
	raw := envelope.Message
	if provider == "codex" {
		if envelope.Type != "response_item" {
			return Message{}, false, false
		}
		raw = envelope.Payload
	}
	var m struct {
		Type    string          `json:"type"`
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return Message{}, false, false
	}
	if provider == "codex" && m.Type != "message" {
		return Message{}, false, false
	}
	if m.Role != "user" && m.Role != "assistant" {
		return Message{}, false, false
	}
	msg := Message{Role: m.Role}
	if json.Unmarshal(m.Content, &msg.Text) == nil {
		return msg, true, false
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &blocks) != nil {
		return Message{}, false, true
	}
	omitted := false
	for _, block := range blocks {
		if block.Type == "text" || block.Type == "input_text" || block.Type == "output_text" {
			if msg.Text != "" {
				msg.Text += "\n"
			}
			msg.Text += block.Text
		} else {
			omitted = true
		}
	}
	return msg, msg.Text != "", omitted
}

func ReadCheckpoint(path, provider string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	reader := bufio.NewReader(f)
	if info.Size() > MaxTranscriptBytes {
		if _, err := f.Seek(info.Size()-MaxTranscriptBytes, io.SeekStart); err != nil {
			return "", err
		}
		if _, err := reader.ReadBytes('\n'); err != nil && err != io.EOF {
			return "", err
		}
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), MaxTranscriptLine)
	checkpoint := ""
	for scanner.Scan() {
		var row struct {
			Type    string `json:"type"`
			Summary string `json:"summary"`
			Payload struct {
				Message string `json:"message"`
			} `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &row) != nil {
			continue
		}
		candidate := ""
		if provider == "claude" && row.Type == "summary" {
			candidate = row.Summary
		}
		if provider == "codex" && row.Type == "compacted" {
			candidate = row.Payload.Message
		}
		if candidate != "" && len(candidate) < MaxPacketBytes/2 {
			checkpoint = candidate
		}
	}
	return checkpoint, nil
}
