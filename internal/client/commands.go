package client

import (
	"agent_romm/internal/protocol"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

type CommandKind string

const (
	CommandPrompt          CommandKind = "submit"
	CommandSteer           CommandKind = "steer"
	CommandNote            CommandKind = "note"
	CommandQueue           CommandKind = "queue"
	CommandStatus          CommandKind = "status"
	CommandWho             CommandKind = "who"
	CommandCancel          CommandKind = "cancel"
	CommandDiff            CommandKind = "diff"
	CommandRecoverRetry    CommandKind = "retry"
	CommandRecoverSkip     CommandKind = "skip"
	CommandRecoverContinue CommandKind = "continue"
	CommandQuit            CommandKind = "quit"
)

type Command struct {
	Kind            CommandKind
	Text            string
	TargetMessageID string
}

func ParseCommand(line string) (Command, error) {
	line = strings.TrimSpace(line)
	bad := errors.New("invalid command or arguments")
	if line == "" {
		return Command{}, bad
	}
	if !strings.HasPrefix(line, "/") {
		return Command{Kind: CommandPrompt, Text: line}, nil
	}
	fields := strings.Fields(line)
	kind := CommandKind(strings.TrimPrefix(fields[0], "/"))
	text := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
	switch kind {
	case CommandSteer, CommandNote:
		if text != "" {
			return Command{Kind: kind, Text: text}, nil
		}
	case CommandQueue, CommandStatus, CommandWho, CommandCancel, CommandDiff, CommandQuit:
		if len(fields) == 1 {
			return Command{Kind: kind}, nil
		}
	case "recover":
		if len(fields) >= 3 && protocol.ValidMessageID(fields[2]) {
			action := CommandKind(fields[1])
			if (action == CommandRecoverRetry || action == CommandRecoverSkip) && len(fields) == 3 {
				return Command{Kind: action, TargetMessageID: fields[2]}, nil
			}
			if action == CommandRecoverContinue && len(fields) > 3 {
				return Command{Kind: action, TargetMessageID: fields[2], Text: strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(text, fields[1])), fields[2]))}, nil
			}
		}
	}
	return Command{}, bad
}
func NewID(random io.Reader) (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(random, b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func request(random io.Reader, method string, body any) (protocol.Envelope, error) {
	id, e := NewID(random)
	if e != nil {
		return protocol.Envelope{}, e
	}
	b, e := json.Marshal(body)
	return protocol.Envelope{Version: 1, Kind: protocol.KindRequest, ID: id, Method: method, Body: b}, e
}
func (cmd Command) Envelope(random io.Reader, active string) (protocol.Envelope, bool, error) {
	mutation := cmd.Kind == CommandPrompt || cmd.Kind == CommandNote || cmd.Kind == CommandSteer || cmd.Kind == CommandCancel || cmd.Kind == CommandRecoverRetry || cmd.Kind == CommandRecoverSkip || cmd.Kind == CommandRecoverContinue
	if (cmd.Kind == CommandSteer || cmd.Kind == CommandCancel) && active == "" {
		return protocol.Envelope{}, false, errors.New("no active turn displayed")
	}
	var body any = protocol.Empty{}
	method := string(cmd.Kind)
	if mutation {
		id, e := NewID(random)
		if e != nil {
			return protocol.Envelope{}, false, e
		}
		switch cmd.Kind {
		case CommandPrompt, CommandNote:
			body = protocol.SubmitRequest{ClientMessageID: id, Text: cmd.Text}
		case CommandSteer:
			body = protocol.SteerRequest{ClientMessageID: id, ExpectedTurnID: active, Text: cmd.Text}
		case CommandCancel:
			body = protocol.CancelRequest{ClientMessageID: id, ExpectedTurnID: active}
		default:
			method = "recover"
			r := protocol.RecoverRequest{ClientMessageID: id, TargetMessageID: cmd.TargetMessageID, Action: string(cmd.Kind), Instruction: cmd.Text}
			if cmd.Kind == CommandRecoverContinue {
				r.ReplacementMessageID, e = NewID(random)
				if e != nil {
					return protocol.Envelope{}, false, e
				}
			}
			body = r
		}
	}
	env, e := request(random, method, body)
	if e == nil && mutation && len(env.Body) > 64<<10 {
		e = errors.New("encoded mutation exceeds 64 KiB; shorten the text")
	}
	return env, mutation, e
}
