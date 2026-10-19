package cli

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tyclab/tycswap/internal/termsafe"
)

type menuPrompt struct {
	title, text          string
	choices              []string
	index, width, height int
	accepted, done       bool
}

func (m menuPrompt) matches() []int {
	var out []int
	for i, label := range m.choices {
		if strings.Contains(strings.ToLower(label), strings.ToLower(m.text)) {
			out = append(out, i)
		}
	}
	return out
}

func (m menuPrompt) Init() tea.Cmd { return nil }

func (m menuPrompt) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch k := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = k.Width, k.Height
	case tea.KeyMsg:
		switch k.String() {
		case "esc", "ctrl+c", "ctrl+d":
			m.done = true
			return m, tea.Quit
		case "enter":
			if m.choices == nil || len(m.matches()) > 0 {
				m.accepted, m.done = true, true
				return m, tea.Quit
			}
		case "up", "shift+tab":
			m.index = max(0, m.index-1)
		case "down", "tab":
			m.index = max(0, min(len(m.matches())-1, m.index+1))
		case "backspace", "ctrl+h":
			runes := []rune(m.text)
			if len(runes) > 0 {
				m.text = string(runes[:len(runes)-1])
			}
			m.index = 0
		case "ctrl+u":
			m.text, m.index = "", 0
		default:
			if k.Type == tea.KeyRunes || k.Type == tea.KeySpace {
				m.text += termsafe.Strip(string(k.Runes))
				m.index = 0
			}
		}
	}
	return m, nil
}

func (m menuPrompt) View() string {
	if m.done {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s\n", termsafe.Strip(m.title))
	fmt.Fprintf(&b, "> %s\n", m.text)
	if m.choices != nil {
		matches := m.matches()
		limit := max(1, min(12, m.height-6))
		start := max(0, m.index-limit+1)
		for n := start; n < min(len(matches), start+limit); n++ {
			prefix := "  "
			if n == m.index {
				prefix = "› "
			}
			label := []rune(termsafe.Strip(m.choices[matches[n]]))
			if len(label) > max(1, m.width-4) {
				label = append(label[:max(1, m.width-5)], '…')
			}
			fmt.Fprintln(&b, prefix+string(label))
		}
		if len(matches) == 0 {
			b.WriteString("  No matches\n")
		}
		b.WriteString("Type to filter · ↑↓ choose · Enter select · Esc back\n")
	} else {
		b.WriteString("Enter save · Ctrl+U clear · Esc back\n")
	}
	return b.String()
}

var askMenu = askTerminal

func askGoMenu(s ioStreams, title string, choices []string, initial string) (int, string, bool) {
	m := menuPrompt{title: title, choices: choices, text: termsafe.Strip(initial), width: 80, height: 24}
	result, err := tea.NewProgram(m, tea.WithInput(s.in), tea.WithOutput(s.err)).Run()
	if err != nil {
		fmt.Fprintln(s.err, err)
		return 0, "", false
	}
	m = result.(menuPrompt)
	if !m.accepted {
		fmt.Fprintf(s.err, "? %s › cancelled\n", termsafe.Strip(title))
		return 0, "", false
	}
	if choices != nil {
		i := m.matches()[m.index]
		fmt.Fprintf(s.err, "? %s › %s\n", termsafe.Strip(title), termsafe.Strip(choices[i]))
		return i, "", true
	}
	fmt.Fprintf(s.err, "? %s › %s\n", termsafe.Strip(title), m.text)
	return 0, m.text, true
}
