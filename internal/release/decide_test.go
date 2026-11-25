package release

import (
	"errors"
	"io"
	"strings"
	"testing"
)

const mergeSHA = "1111111111111111111111111111111111111111"

func gateSays(v Verdict, why ...string) func() (Verdict, []string, error) {
	return func() (Verdict, []string, error) { return v, why, nil }
}

func missing(lw, darwin bool) func() (Missing, error) {
	return func() (Missing, error) { return Missing{lw, darwin}, nil }
}

func unasked(t *testing.T) (func() (Verdict, []string, error), func() (Missing, error)) {
	return func() (Verdict, []string, error) {
			t.Error("CI was asked")
			return Block, nil, nil
		}, func() (Missing, error) {
			t.Error("the release was asked")
			return Missing{}, nil
		}
}

func TestDecide(t *testing.T) {
	noGate, noRelease := unasked(t)
	cases := []struct {
		name    string
		f       Facts
		want    Plan
		wantErr string
	}{
		{"a merge that names no version", Facts{Mode: ModeMerge, Commit: mergeSHA, Gate: noGate, Missing: noRelease}, Plan{}, ""},
		{"CI passed: tag and release everything", Facts{Mode: ModeMerge, Commit: mergeSHA, Version: "v0.24.0", Attempt: 1, Gate: gateSays(Pass), Missing: noRelease},
			Plan{Tag: "v0.24.0", Commit: mergeSHA, CreateTag: true, Publish: true, LinuxWindows: true, Darwin: true}, ""},
		{"macOS found no runner: same", Facts{Mode: ModeMerge, Commit: mergeSHA, Version: "v0.24.0", Attempt: 1, Gate: gateSays(Pass, "macos found no runner"), Missing: noRelease},
			Plan{Tag: "v0.24.0", Commit: mergeSHA, CreateTag: true, Publish: true, LinuxWindows: true, Darwin: true}, ""},
		{"windows found no runner: wait", Facts{Mode: ModeMerge, Commit: mergeSHA, Version: "v0.24.0", Attempt: 1, Gate: gateSays(Wait), Missing: noRelease}, Plan{}, ""},
		{"CI failed: refuse loudly", Facts{Mode: ModeMerge, Commit: mergeSHA, Version: "v0.24.0", Attempt: 1, Gate: gateSays(Block, "macos ended failure"), Missing: noRelease},
			Plan{}, "macos ended failure"},
		{"tag elsewhere", Facts{Mode: ModeMerge, Commit: mergeSHA, Version: "v0.24.0", TagCommit: "2222222222222222", Gate: noGate, Missing: noRelease},
			Plan{}, "tag v0.24.0 points at 222222222222"},
		{"a fresh run for a tagged merge leaves it to the tagging run", Facts{Mode: ModeMerge, Commit: mergeSHA, Version: "v0.24.0", TagCommit: mergeSHA, Attempt: 1, Gate: noGate, Missing: noRelease}, Plan{}, ""},
		{"a rerun finishes macOS", Facts{Mode: ModeMerge, Commit: mergeSHA, Version: "v0.24.0", TagCommit: mergeSHA, Attempt: 2, Gate: noGate, Missing: missing(false, true)},
			Plan{Tag: "v0.24.0", Commit: mergeSHA, Publish: true, Darwin: true}, ""},
		{"a rerun of a finished release does nothing", Facts{Mode: ModeMerge, Commit: mergeSHA, Version: "v0.24.0", TagCommit: mergeSHA, Attempt: 3, Gate: noGate, Missing: missing(false, false)}, Plan{}, ""},
		{"a hand tag builds a draft", Facts{Mode: ModeTag, Commit: mergeSHA, Version: "v0.24.0", TagCommit: mergeSHA, Attempt: 1, HasNotes: true, Gate: noGate, Missing: missing(true, true)},
			Plan{Tag: "v0.24.0", Commit: mergeSHA, LinuxWindows: true, Darwin: true}, ""},
		{"a dispatch publishes a tag with notes", Facts{Mode: ModeDispatch, Commit: mergeSHA, Version: "v0.24.0", TagCommit: mergeSHA, Attempt: 1, HasNotes: true, Gate: noGate, Missing: missing(true, false)},
			Plan{Tag: "v0.24.0", Commit: mergeSHA, Publish: true, LinuxWindows: true}, ""},
		{"a dispatch for a tag without notes keeps a draft", Facts{Mode: ModeDispatch, Commit: mergeSHA, Version: "v0.24.0", TagCommit: mergeSHA, Attempt: 1, Gate: noGate, Missing: missing(true, true)},
			Plan{Tag: "v0.24.0", Commit: mergeSHA, LinuxWindows: true, Darwin: true}, ""},
		{"a dispatch for no tag", Facts{Mode: ModeDispatch, Version: "v9.9.9", Gate: noGate, Missing: noRelease}, Plan{}, "there is no tag v9.9.9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Decide(tc.f, io.Discard)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("Decide = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestDecidePassesLookupErrorsOn(t *testing.T) {
	boom := errors.New("api down")
	_, err := Decide(Facts{Mode: ModeMerge, Commit: mergeSHA, Version: "v0.24.0", Attempt: 1,
		Gate: func() (Verdict, []string, error) { return Pass, nil, boom }}, io.Discard)
	if !errors.Is(err, boom) {
		t.Errorf("error %v", err)
	}
}

func TestMissingFrom(t *testing.T) {
	const tag = "v0.24.0"
	all := append(LinuxWindowsAssets(tag), DarwinAssets(tag)...)
	sums := map[string]string{}
	for _, n := range all {
		sums[n] = "h"
	}
	if m := MissingFrom(tag, all, sums); m.LinuxWindows || m.Darwin {
		t.Errorf("complete: %+v", m)
	}
	if m := MissingFrom(tag, LinuxWindowsAssets(tag), sums); m.LinuxWindows || !m.Darwin {
		t.Errorf("no macOS: %+v", m)
	}
	delete(sums, DarwinAssets(tag)[1])
	if m := MissingFrom(tag, all, sums); !m.Darwin {
		t.Errorf("a macOS build without its line: %+v", m)
	}
	var plan strings.Builder
	Plan{Tag: tag, Commit: mergeSHA, Publish: true, Darwin: true}.Write(&plan)
	if !strings.Contains(plan.String(), "tag=v0.24.0\n") || !strings.Contains(plan.String(), "darwin=true\n") || !strings.Contains(plan.String(), "linux-windows=false\n") {
		t.Errorf("plan output %q", plan.String())
	}
}
