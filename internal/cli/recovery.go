package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/procdetect"
	"github.com/tyclab/tycswap/internal/recovery"
	"github.com/tyclab/tycswap/internal/session"
)

func recoveryCommand(prog string, argv []string, streams ioStreams) int {
	if len(argv) == 0 {
		fmt.Fprintln(streams.err, "Usage: "+prog+" recovery record|observe|list|dismiss|prepare|plan|reclaim")
		return 2
	}
	store := recovery.NewStore(paths.GetBackupRoot())
	fs := flag.NewFlagSet("recovery "+argv[0], flag.ContinueOnError)
	fs.SetOutput(streams.err)
	group := fs.String("group", "", "Managed session group")
	sourcePID := fs.Int("source-pid", 0, "Owned app-server process PID, captured while running")
	accountID := fs.String("account", "", "Host-bound app-server account slot")
	accountIdentity := fs.String("account-identity", "", "Host-bound account identity hash")
	model := fs.String("model", "", "Known selected model")
	provider := fs.String("provider", "claude", "claude hook or codex app-server")
	sessionID := fs.String("session", "", "Source session ID")
	incidentID := fs.String("incident", "", "Incident ID")
	transcript := fs.String("transcript", "", "Explicitly selected saved transcript")
	cwd := fs.String("cwd", "", "Source workspace absolute path")
	objective := fs.String("objective", "", "Saved objective")
	backgroundConfirmed := fs.Bool("confirm-no-background-tools", false, "Confirm background commands and tools are stopped")
	checkpoint := fs.String("checkpoint-file", "", "Saved checkpoint file")
	if err := fs.Parse(argv[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(streams.err, "Unexpected arguments")
		return 2
	}
	output := func(value any, err error) int {
		if err != nil {
			fmt.Fprintln(streams.err, err)
			return 1
		}
		if err := json.NewEncoder(streams.out).Encode(value); err != nil {
			fmt.Fprintln(streams.err, err)
			return 1
		}
		return 0
	}
	switch argv[0] {
	case "record":
		// Lifecycle hooks fail open: recorder errors never stop the source session.
		data, err := io.ReadAll(io.LimitReader(streams.in, recovery.MaxEventBytes+1))
		if err != nil {
			fmt.Fprintln(streams.err, err)
			return 0
		}
		var event recovery.Event
		switch *provider {
		case "claude":
			event, err = recovery.ParseClaude(data, *group, *model, time.Now())
		case "codex":
			event, err = recovery.ParseCodex(data, *model, time.Now())
		default:
			err = fmt.Errorf("unsupported provider %q", *provider)
		}
		if err == nil {
			if event.Provider == "claude" && (event.Kind == "UserPromptSubmit" || event.Kind == "SessionStart") && event.Group != "" {
				if event.Kind == "SessionStart" && os.Getenv("TYCSWAP_GROUP_LAUNCH_ID") != "" {
					event.ModelObserved = true
				}
				if active, aerr := groups.LoadActive(paths.GetBackupRoot(), groups.ID(event.Group)); aerr == nil && active != nil {
					event.AccountID = active.Number
					event.AccountIdentity = recovery.IdentityKey(active.Email, active.OrgUUID)
				}
			}
			if event.Provider == "codex" && (event.Kind == "turn/started" || event.Kind == "thread/started") {
				event.AccountID = *accountID
				event.AccountIdentity = *accountIdentity
				if procdetect.IsPIDAlive(*sourcePID) {
					event.SourcePID = *sourcePID
				}
			}
		}
		if err == nil && event.Kind == "UserPromptSubmit" {
			block, guardErr := store.EditBlock(event.SessionID, event.CWD)
			if guardErr == nil && block != "" {
				fmt.Fprintln(streams.err, block)
				return 2
			}
		}
		if err == nil {
			event, err = store.Record(event)
		}
		if err == nil && event.Kind == "SessionStart" && event.Provider == "claude" && event.Group != "" {
			err = session.RecordGroupSession(paths.GetBackupRoot(), groups.ID(event.Group), os.Getenv("TYCSWAP_GROUP_LAUNCH_ID"), event.SessionID, event.CWD)
		}
		if err == nil && (event.Kind == "SessionStart" || event.Kind == "thread/started") {
			err = store.ConfirmDestination(event, os.Getenv("TYCSWAP_HANDOVER_CWD"), os.Getenv("TYCSWAP_HANDOVER_DIGEST"))
		}
		if errors.Is(err, recovery.ErrEditOwned) {
			fmt.Fprintln(streams.err, "Workspace editing ownership is transferred; stop the destination and use tycswap recovery reclaim --cwd before continuing.")
			return 2
		}
		if err != nil {
			fmt.Fprintln(streams.err, "Recovery recording: "+err.Error())
		}
		return 0
	case "observe":
		if *provider != "codex" {
			return output(nil, fmt.Errorf("observe requires --provider codex and app-server JSONL"))
		}
		pid := 0
		if procdetect.IsPIDAlive(*sourcePID) {
			pid = *sourcePID
		}
		err := store.ObserveCodexOwned(streams.in, *model, *accountID, *accountIdentity, pid, nil)
		return output(map[string]bool{"observed": err == nil}, err)
	case "reclaim":
		err := reclaimHandover(store, *cwd, *backgroundConfirmed)
		return output(map[string]bool{"reclaimed": err == nil}, err)
	case "list":
		state, err := store.Snapshot()
		return output(state, err)
	case "dismiss":
		err := store.Dismiss(*sessionID, *incidentID)
		return output(map[string]bool{"dismissed": err == nil}, err)
	case "prepare":
		var source recovery.Source
		if *sessionID != "" && *cwd != "" {
			source = recovery.Source{Provider: *provider, SessionID: *sessionID, CWD: *cwd, Group: *group, Objective: *objective}
		} else if err := json.NewDecoder(io.LimitReader(streams.in, 2*recovery.MaxPacketBytes)).Decode(&source); err != nil {
			return output(nil, err)
		}
		if *transcript != "" {
			messages, omissions, err := recovery.ReadTranscript(*transcript, source.Provider)
			if err != nil {
				return output(nil, err)
			}
			source.Messages = messages
			source.Omissions = append(source.Omissions, omissions...)
			if source.Checkpoint == "" {
				saved, err := recovery.ReadCheckpoint(*transcript, source.Provider)
				if err == nil {
					source.Checkpoint = saved
				}
			}
		}
		if *checkpoint != "" {
			file, err := os.Open(*checkpoint)
			if err != nil {
				return output(nil, err)
			}
			data, err := io.ReadAll(io.LimitReader(file, recovery.MaxPacketBytes+1))
			file.Close()
			if err != nil {
				return output(nil, err)
			}
			if len(data) > recovery.MaxPacketBytes {
				return output(nil, fmt.Errorf("checkpoint exceeds packet limit"))
			}
			source.Checkpoint = string(data)
		}
		packet, err := recovery.Prepare(source)
		return output(packet, err)
	case "plan":
		var input struct {
			Packet      recovery.Packet       `json:"packet"`
			Destination recovery.Destination  `json:"destination"`
			Request     recovery.StartRequest `json:"request"`
		}
		if err := json.NewDecoder(io.LimitReader(streams.in, 2*recovery.MaxPacketBytes)).Decode(&input); err != nil {
			return output(nil, err)
		}
		plan, err := recovery.Plan(input.Packet, input.Destination, input.Request)
		return output(plan, err)
	default:
		fmt.Fprintln(streams.err, "Unknown recovery command: "+argv[0])
		return 2
	}
}
