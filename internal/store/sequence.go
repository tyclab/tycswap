package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/cerr"
)

type SequenceData struct {
	ActiveAccountNumber *int                       `json:"activeAccountNumber"`
	LastUpdated         string                     `json:"lastUpdated"`
	Sequence            []int                      `json:"sequence"`
	Accounts            map[string]json.RawMessage `json:"accounts"`
}

// ReadSequence reads and parses sequence.json (spec 01§2.3 _read_json). A
// missing file returns (nil, nil); so do malformed JSON (logging an "Invalid
// JSON in {path}" warning) and the literal null, which parses but is no roster —
// all of them are Python's None outcome. A non-NotExist read failure propagates
// as an error (Python lets it raise).
//
// Callers that are about to WRITE the roster must NOT start from this read: it
// cannot tell a fresh install from a corrupted file, and the two demand opposite
// answers. Use SequenceForUpdate.
func (s *Store) ReadSequence() (*SequenceData, error) {
	data, _, _, err := s.readSequenceState()
	return data, err
}

type sequenceState int

const (
	seqParsed     sequenceState = iota // the file parsed into a roster
	seqAbsent                          // no file at all: a fresh install
	seqUnreadable                      // the file is there but yields no roster: corruption
)

func (s *Store) readSequenceState() (*SequenceData, sequenceState, string, error) {
	raw, err := os.ReadFile(s.SequenceFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, seqAbsent, "", nil
		}
		return nil, seqUnreadable, "is unreadable (" + readFailureDetail(err) + ")", err
	}
	// Decoding through a POINTER separates the two JSON documents that unmarshal
	// into a roster struct without error: an object (pointer allocated) and the
	// literal null (pointer left nil). null is well-formed JSON and no roster —
	// Python's json.loads answers None for it too — so it must not become an
	// empty roster a writer then renames over the real records.
	var sd *SequenceData
	if err := json.Unmarshal(raw, &sd); err != nil {
		s.warnf("Invalid JSON in %s", s.SequenceFile)
		return nil, seqUnreadable, "is not valid JSON", nil
	}
	if sd == nil {
		s.warnf("No account roster in %s: the file holds JSON null", s.SequenceFile)
		return nil, seqUnreadable, "is valid JSON but holds null instead of an account roster", nil
	}
	return sd, seqParsed, "", nil
}

func readFailureDetail(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// warnf logs a warning when a logger is attached (early construction steps run
// before one is).
func (s *Store) warnf(format string, a ...any) {
	if s.Log != nil {
		s.Log.Warningf(format, a...)
	}
}

func (s *Store) SequenceForUpdate() (*SequenceData, error) {
	data, err := s.classifiedRoster()
	if err != nil {
		return nil, err
	}
	if data == nil {
		return s.emptySequence(), nil
	}
	return normalizeRoster(data), nil
}

// classifiedRoster is the classification itself, shared by every entry read that
// can end in a write — SequenceForUpdate and the org-field backfill's own read
// alike. Present-but-not-a-roster is the corruption refusal; absence is reported
// as (nil, nil) so each caller says what absence means for IT, an empty roster
// for a writer and Python's None for the backfill, without either one having to
// re-derive which of the two happened.
func (s *Store) classifiedRoster() (*SequenceData, error) {
	data, state, diagnosis, readErr := s.readSequenceState()
	switch state {
	case seqAbsent:
		return nil, nil
	case seqUnreadable:
		return nil, s.corruptSequenceError(diagnosis, readErr)
	}
	return data, nil
}

func (s *Store) MigratedSequenceForUpdate() (*SequenceData, error) {
	data, err := s.SequenceMigrated()
	return s.rosterForUpdate(data, err)
}

// MigratedSequenceForUpdateLocked is for callers already holding the store
// lock, including import's write pass. It must not acquire it again.
func (s *Store) MigratedSequenceForUpdateLocked() (*SequenceData, error) {
	data, err := s.sequenceMigratedLocked()
	return s.rosterForUpdate(data, err)
}

func (s *Store) rosterForUpdate(data *SequenceData, err error) (*SequenceData, error) {
	if err != nil {
		return nil, err
	}
	if data == nil {
		return s.emptySequence(), nil
	}
	return normalizeRoster(data), nil
}

// normalizeRoster materializes the two containers a write path assigns into. A
// parsed object carrying no "accounts" / "sequence" key holds no records and no
// rotation order, so filling them in empty loses nothing — and it is the
// difference between recording a new account and an "assignment to entry in nil
// map" panic that aborts AFTER the credential and config backups were written,
// leaving them orphaned.
func normalizeRoster(data *SequenceData) *SequenceData {
	if data.Accounts == nil {
		data.Accounts = map[string]json.RawMessage{}
	}
	if data.Sequence == nil {
		data.Sequence = []int{}
	}
	return data
}

// corruptSequenceError is the single refusal for a sequence.json that exists but
// is not a roster: what is wrong, what is NOT lost, and the two ways out. Only
// the diagnosis clause varies, because the user's position and remedy are
// identical however the file failed — but it must vary, and it must be the
// reading's own finding. A file whose whole content is the literal null IS valid
// JSON, so diagnosing it as a syntax error sends the user hunting through a file
// for a fault it does not have. cause is the read failure when the bytes could
// not be obtained at all, nil when they were read but are no roster.
func (s *Store) corruptSequenceError(diagnosis string, cause error) error {
	err := cerr.Config(
		"%s %s, so the accounts it lists cannot be read — refusing to overwrite it. "+
			"Every stored credential and config backup is intact, but this file is the only thing that names them. "+
			"Repair the file (the records are plain text; a JSON editor or a copy of the file will do) and retry, "+
			"or delete it to start a fresh roster and re-register each account with `tycswap add`.",
		s.SequenceFile, diagnosis)
	if cause != nil {
		return err.Wrap(cause)
	}
	return err
}

func (s *Store) emptySequence() *SequenceData {
	return &SequenceData{
		ActiveAccountNumber: nil,
		LastUpdated:         s.timestamp(),
		Sequence:            []int{},
		Accounts:            map[string]json.RawMessage{},
	}
}

// WriteSequence renders data as two-space-indented JSON (Python parity), rejects
// a non-round-tripping result with ConfigError("Generated invalid JSON"), then
// writes it atomically (temp-in-dir → chmod 0600 → rename; parent chmod 0700 on
// non-Windows). Mirrors _write_json (spec 01§2.3).
//
// Every committed write (a switch's commit and rollback included) then
// rebuilds statusline.json, best-effort (DESIGN A54).
func (s *Store) WriteSequence(data *SequenceData) error {
	if err := s.writeSequenceFile(data); err != nil {
		return err
	}
	s.PublishStatusline()
	return nil
}

func (s *Store) writeSequenceFile(data *SequenceData) error {
	encoded, err := marshalIndent2(data)
	if err != nil {
		return err
	}
	var probe any
	if err := json.Unmarshal(encoded, &probe); err != nil {
		return cerr.Config("Generated invalid JSON").Wrap(err)
	}
	return atomicfile.Write(s.SequenceFile, encoded, atomicfile.Opts{})
}

func (s *Store) InitSequenceFile() error {
	if _, err := os.Stat(s.SequenceFile); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return s.WriteSequence(s.emptySequence())
}

func (s *Store) NextAccountNumberFrom(data *SequenceData) int {
	if data == nil || len(data.Accounts) == 0 {
		return 1
	}
	max := 0
	for num := range data.Accounts {
		if n, ok := atoiSlot(num); ok && n > max {
			max = n
		}
	}
	return max + 1
}

func (s *Store) NextAccountNumber() int {
	data, _ := s.ReadSequence()
	return s.NextAccountNumberFrom(data)
}

func marshalIndent2(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	return b, nil
}

// decodeRecord unmarshals an account record into a mutable map for field reads
// and edits. Anything that is not a JSON object yields an empty map — never nil,
// and never a half-filled one — so callers can both index it and ASSIGN into it
// safely. The literal null is the case that makes this a contract rather than a
// formality: it unmarshals into a map target by setting the map to nil (unlike
// every other malformed record, which leaves the initialized map alone), and the
// backfills that write org and uuid fields assign into whatever this returns.
func decodeRecord(raw json.RawMessage) map[string]any {
	m := map[string]any{}
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return map[string]any{}
	}
	return m
}

func encodeRecord(rec map[string]any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(rec); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

func recordFor(data *SequenceData, num string) (map[string]any, bool) {
	if data == nil {
		return nil, false
	}
	raw, ok := data.Accounts[num]
	if !ok {
		return nil, false
	}
	return decodeRecord(raw), true
}

func strField(rec map[string]any, key string) string {
	if v, ok := rec[key].(string); ok {
		return v
	}
	return ""
}

func strOrEmpty(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func atoiSlot(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
