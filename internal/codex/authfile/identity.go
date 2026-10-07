package authfile

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/platform"
)

// AuthClaim is the namespaced claim block the ChatGPT tokens carry their
// account context in.
const AuthClaim = "https://api.openai.com/auth"

// Identity is who an auth.json payload belongs to.
type Identity struct {
	AccountID string
	UserID    string
	Email     string
	Plan      string
	IsAPIKey  bool
}

// AccountKey is the stable identity key, byte-identical to codex-auth's.
func (i Identity) AccountKey() string {
	return AccountKey(i.UserID, i.AccountID)
}

func (i Identity) Identifiable() bool {
	return i.AccountID != ""
}

// AccountKey joins a user and account id into codex-auth's record key.
func AccountKey(userID, accountID string) string {
	return userID + "::" + accountID
}

// FileKey encodes an account key for use as a filename or Keychain account:
// unpadded base64url, matching codex-auth's snapshot filenames (verified
// against a real <key>.auth.json on disk).
func FileKey(key string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(key))
}

// DecodeJWTClaims decodes a JWT's payload segment without verifying it. It
// returns nil unless token is a string with at least two dot-separated
// segments whose second segment is base64url of UTF-8 JSON encoding an object.
func DecodeJWTClaims(token any) map[string]any {
	s, ok := token.(string)
	if !ok {
		return nil
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return nil
	}
	raw, err := decodeSegment(parts[1])
	if err != nil {
		return nil
	}
	v, err := decodeJSON(raw, false)
	if err != nil {
		return nil
	}
	claims, _ := v.(map[string]any)
	return claims
}

// decodeSegment decodes a JWT segment the way Python's urlsafe_b64decode does
// after re-padding: padding is optional, and the standard alphabet's + and /
// are accepted alongside - and _ (urlsafe_b64decode only translates, it does
// not reject them).
func decodeSegment(seg string) ([]byte, error) {
	seg = strings.TrimRight(seg, "=")
	seg = strings.NewReplacer("+", "-", "/", "_").Replace(seg)
	return base64.RawURLEncoding.DecodeString(seg)
}

// decodeJSON parses exactly one JSON value from raw. Invalid UTF-8 is an error
// (Python's strict decode raises where encoding/json would silently substitute
// U+FFFD), and so is trailing non-whitespace. useNumber keeps numbers as
// json.Number so a payload re-written later round-trips its integers exactly.
func decodeJSON(raw []byte, useNumber bool) (any, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("invalid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if useNumber {
		dec.UseNumber()
	}
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data after JSON value")
	}
	return v, nil
}

// truthy mirrors Python truthiness for JSON-decoded values.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case json.Number:
		f, err := x.Float64()
		return err != nil || f != 0
	case map[string]any:
		return len(x) > 0
	case []any:
		return len(x) > 0
	default:
		return true
	}
}

// pyStr renders a JSON-decoded value the way Python's str() would render the
// value json.loads produced. An integral float64 prints as an integer because
// Python decodes an integer literal as int; only a caller feeding exotic claim
// types (numbers, booleans) as ids ever reaches past the string case.
func pyStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case json.Number:
		return x.String()
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return strconv.FormatFloat(x, 'f', -1, 64)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}

// orgFallback picks an organization id when no account id is present: the
// first is_default org with an id, else the first org with an id.
func orgFallback(authClaims map[string]any) string {
	orgs, ok := authClaims["organizations"].([]any)
	if !ok {
		return ""
	}
	for _, o := range orgs {
		if org, ok := o.(map[string]any); ok && truthy(org["is_default"]) && truthy(org["id"]) {
			return pyStr(org["id"])
		}
	}
	for _, o := range orgs {
		if org, ok := o.(map[string]any); ok && truthy(org["id"]) {
			return pyStr(org["id"])
		}
	}
	return ""
}

// firstTruthy returns the first truthy value, or "" (Python's `a or b or ""`).
func firstTruthy(vs ...any) any {
	for _, v := range vs {
		if truthy(v) {
			return v
		}
	}
	return ""
}

// ParseIdentity derives an Identity from an auth.json payload. It returns nil
// when payload is not an object, or when it carries tokens whose JWTs cannot
// be decoded.
func ParseIdentity(payload any) *Identity {
	p, ok := payload.(map[string]any)
	if !ok {
		return nil
	}

	tokens, ok := p["tokens"].(map[string]any)
	if !ok {
		// An API-key login has no OAuth tokens at all. It is a real, listable
		// account — it simply has no usage and nothing to refresh — so it gets
		// an identity rather than a parse failure.
		if p["auth_mode"] == "apikey" || truthy(p["OPENAI_API_KEY"]) {
			return &Identity{IsAPIKey: true}
		}
		return nil
	}

	// Python's `decode(id_token) or decode(access_token)`: an empty claim set
	// from the id token falls through to the access token's result, even if
	// that result is nil.
	claims := DecodeJWTClaims(tokens["id_token"])
	if len(claims) == 0 {
		claims = DecodeJWTClaims(tokens["access_token"])
	}
	if claims == nil {
		return nil
	}

	authClaims, ok := claims[AuthClaim].(map[string]any)
	if !ok {
		authClaims = map[string]any{}
	}

	accountID := pyStr(firstTruthy(tokens["account_id"], authClaims["chatgpt_account_id"]))
	if accountID == "" {
		accountID = orgFallback(authClaims)
	}

	return &Identity{
		AccountID: accountID,
		UserID:    pyStr(firstTruthy(authClaims["chatgpt_user_id"])),
		Email:     pyStr(firstTruthy(claims["email"])),
		Plan:      pyStr(firstTruthy(authClaims["chatgpt_plan_type"])),
		IsAPIKey:  p["auth_mode"] == "apikey",
	}
}

// ReadLivePayload reads the live auth.json. It returns nil when the file is
// absent, unreadable, torn, or not a JSON object: a torn write is transient —
// the next pass re-reads — so it is never fatal. Numbers decode as
// json.Number so a payload written back through WriteLiveAuth is unchanged.
func ReadLivePayload() map[string]any {
	raw, err := os.ReadFile(LiveAuthPath())
	if err != nil {
		return nil
	}
	v, err := decodeJSON(raw, true)
	if err != nil {
		return nil
	}
	m, _ := v.(map[string]any)
	return m
}

// ReadLiveIdentity returns the identity of whoever is currently logged in to
// the codex CLI, or nil.
func ReadLiveIdentity() *Identity {
	payload := ReadLivePayload()
	if payload == nil {
		return nil
	}
	return ParseIdentity(payload)
}

// AccessTokenExpiry returns the access token's exp claim as POSIX seconds, or
// nil when the payload has no decodable access token or exp is not a number.
func AccessTokenExpiry(payload any) *float64 {
	p, ok := payload.(map[string]any)
	if !ok {
		return nil
	}
	tokens, ok := p["tokens"].(map[string]any)
	if !ok {
		return nil
	}
	claims := DecodeJWTClaims(tokens["access_token"])
	if len(claims) == 0 {
		return nil
	}
	var exp float64
	switch x := claims["exp"].(type) {
	case float64:
		exp = x
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return nil
		}
		exp = f
	default:
		return nil
	}
	return &exp
}

// WriteLiveAuth writes payload to the live auth.json, atomically, and returns
// its path.
//
// The file holds bearer tokens, so it is never left readable by anyone but
// its owner: an existing file keeps its owner bits (mode & 0600, so a codex
// CLI that chose 0400 is not widened) and loses any group or world bits, and a
// file created from scratch is 0600. The codex home directory is the codex
// CLI's: an existing one keeps whatever mode it already has — atomicfile
// chmods the parent to Opts.DirMode, so that is pinned to the directory's
// current mode rather than the 0700 default — and a missing one is created
// private (0700).
func WriteLiveAuth(payload map[string]any) (string, error) {
	path := LiveAuthPath()
	dir := filepath.Dir(path)
	// MkdirAll leaves an existing directory's mode alone.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create codex home: %w", err)
	}

	opts := atomicfile.Opts{FileMode: 0o600}
	if !platform.IsWindows() {
		if fi, err := os.Stat(dir); err == nil {
			opts.DirMode = fi.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
		}
		if fi, err := os.Stat(path); err == nil {
			// A zero mode would read as "use the default" (0600) to
			// atomicfile, which is also the right answer for a file whose
			// owner bits were empty.
			if m := fi.Mode().Perm() & 0o600; m != 0 {
				opts.FileMode = m
			}
		}
	}

	if err := atomicfile.WriteJSON(path, payload, opts); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}
