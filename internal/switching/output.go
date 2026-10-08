// output.go — the small human-output and string helpers switching uses in
// non-JSON mode. Human text goes to stdout (matching Python's print()); warnings
// go to stdout in yellow via printer.Warning (spec DESIGN §3.2). Errors are
// returned as cerr values, never printed here.
package switching

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/tyclab/tycswap/internal/printer"
)

func printOut(text string) { fmt.Fprintln(os.Stdout, text) }

// printWarning writes a yellow warning line to stdout (printer.Warning /
// Python warning()).
func printWarning(msg string) { printer.Warning(msg) }

func lower(s string) string { return strings.ToLower(s) }

func joinComma(parts []string) string { return strings.Join(parts, ", ") }

// joinInts renders ints joined by ", " (for the session-mode PID list).
func joinInts(nums []int) string {
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = itoa(n)
	}
	return strings.Join(parts, ", ")
}

func encodeRec(rec map[string]any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(rec); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n")), nil
}
