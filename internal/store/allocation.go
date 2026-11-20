package store

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const ModelsKey = "models"

var knownModels = map[string]bool{"mythos": true, "fable": true, "opus": true}

// normalModels is set sorted and without duplicates; ok is false for an
// unknown name.
func normalModels(set []string) ([]string, string, bool) {
	seen := map[string]bool{}
	out := []string{}
	for _, m := range set {
		if !knownModels[m] {
			return nil, m, false
		}
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out, "", true
}

// ParseModels reads a comma list of models in any order; it may be empty.
func ParseModels(list string) ([]string, error) {
	var set []string
	for _, part := range strings.Split(list, ",") {
		if part = strings.TrimSpace(part); part != "" {
			set = append(set, part)
		}
	}
	out, bad, ok := normalModels(set)
	if !ok {
		return nil, fmt.Errorf("unknown model: %s (mythos, fable or opus)", bad)
	}
	return out, nil
}

// ModelsFrom is account num's models key: a non-empty list of known models,
// sorted. Anything else, an absent key included, is no key.
func ModelsFrom(data *SequenceData, num string) ([]string, bool) {
	rec, ok := recordFor(data, num)
	if !ok {
		return nil, false
	}
	raw, err := json.Marshal(rec[ModelsKey])
	var set []string
	if err != nil || json.Unmarshal(raw, &set) != nil || len(set) == 0 {
		return nil, false
	}
	out, _, ok := normalModels(set)
	return out, ok
}
