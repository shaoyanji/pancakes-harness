package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"pancakes-harness/internal/assembler"
	"pancakes-harness/internal/backend"
	"pancakes-harness/internal/eventlog"
	"pancakes-harness/internal/model"
	"pancakes-harness/internal/replay"
	"pancakes-harness/internal/tools"
)

var (
	ErrInvalidConfig          = errors.New("invalid runtime config")
	ErrPacketBudgetRejected   = errors.New("packet budget rejected")
	ErrNoToolRunnerConfigured = errors.New("tool runner not configured")
	ErrMaxReasoningTurns      = errors.New("max reasoning turns reached")
)

type Config struct {
	SessionID         string
	DefaultBranchID   string
	Backend           backend.Backend
	ModelAdapter      model.Adapter
	ToolRunner        *tools.Runner
	ModelHeaders      []assembler.Header
	MaxReasoningTurns int
}

type Session struct {
	id                string
	defaultBranchID   string
	backend           backend.Backend
	modelAdapter      model.Adapter
	toolRunner        *tools.Runner
	modelHeaders      []assembler.Header
	maxReasoningTurns int

	mu      sync.Mutex
	counter int
}

type TurnResult struct {
	SessionID           string
	BranchID            string
	Answer              string
	Decision            string
	PacketEnvelopeBytes int
}

type ReplayResult struct {
	SessionState replay.SessionState
	Branches     map[string]string
}

func StartSession(cfg Config) (*Session, error) {
	if cfg.SessionID == "" || cfg.Backend == nil || cfg.ModelAdapter == nil {
		return nil, ErrInvalidConfig
	}
	branch := cfg.DefaultBranchID
	if branch == "" {
		branch = "main"
	}
	maxTurns := cfg.MaxReasoningTurns
	if maxTurns <= 0 {
		maxTurns = 4
	}

	s := &Session{
		id:                cfg.SessionID,
		defaultBranchID:   branch,
		backend:           cfg.Backend,
		modelAdapter:      cfg.ModelAdapter,
		toolRunner:        cfg.ToolRunner,
		modelHeaders:      append([]assembler.Header(nil), cfg.ModelHeaders...),
		maxReasoningTurns: maxTurns,
	}

	events, err := s.backend.ListEventsBySession(context.Background(), s.id)
	if err != nil {
		return nil, err
	}
	s.counter = len(events)
	return s, nil
}

func (s *Session) HandleUserTurn(ctx context.Context, branchID, text string) (TurnResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if branchID == "" {
		branchID = s.defaultBranchID
	}
	now := time.Now().UTC()
	userEvent := eventlog.Event{
		ID:        s.nextEventID("turn.user"),
		SessionID: s.id,
		TS:        now,
		Kind:      eventlog.KindTurnUser,
		BranchID:  branchID,
		Meta: map[string]any{
			"text": text,
		},
	}
	if err := s.backend.AppendEvent(ctx, userEvent); err != nil {
		return TurnResult{}, err
	}

	var lastPacketBytes int
	for i := 0; i < s.maxReasoningTurns; i++ {
		packet, err := s.assembleForBranch(ctx, branchID)
		if err != nil {
			return TurnResult{}, err
		}
		lastPacketBytes = packet.Measurement.EnvelopeBytes

		modelEventID := s.nextEventID("response")
		callReq := model.Request{
			SessionID: s.id,
			BranchID:  branchID,
			Packet:    packet.BodyJSON,
		}

		packetSent := eventlog.Event{
			ID:        s.nextEventID("packet.sent"),
			SessionID: s.id,
			TS:        time.Now().UTC(),
			Kind:      eventlog.KindPacketSent,
			BranchID:  branchID,
			Meta: map[string]any{
				"compact_stage":  strconv.Itoa(packet.Stage),
				"envelope_bytes": strconv.Itoa(packet.Measurement.EnvelopeBytes),
			},
		}
		if err := s.backend.AppendEvent(ctx, packetSent); err != nil {
			return TurnResult{}, err
		}

		callResult, callErr := model.ExecuteAndPersist(ctx, s.backend, s.modelAdapter, callReq, modelEventID, time.Now().UTC())
		if callErr != nil {
			return TurnResult{
				SessionID:           s.id,
				BranchID:            branchID,
				PacketEnvelopeBytes: lastPacketBytes,
			}, callErr
		}

		switch callResult.Response.Decision {
		case "answer":
			agent := eventlog.Event{
				ID:        s.nextEventID("turn.agent"),
				SessionID: s.id,
				TS:        time.Now().UTC(),
				Kind:      eventlog.KindTurnAgent,
				BranchID:  branchID,
				Meta: map[string]any{
					"text": callResult.Response.Answer,
				},
			}
			if err := s.backend.AppendEvent(ctx, agent); err != nil {
				return TurnResult{}, err
			}
			return TurnResult{
				SessionID:           s.id,
				BranchID:            branchID,
				Answer:              callResult.Response.Answer,
				Decision:            callResult.Response.Decision,
				PacketEnvelopeBytes: lastPacketBytes,
			}, nil
		case "tool_calls":
			if err := s.executeToolCalls(ctx, branchID, callResult.Response.ToolCalls); err != nil {
				return TurnResult{}, err
			}
		case "continue":
			continue
		default:
			return TurnResult{}, model.ErrMalformedModelResponse
		}
	}

	return TurnResult{
		SessionID:           s.id,
		BranchID:            branchID,
		PacketEnvelopeBytes: lastPacketBytes,
	}, ErrMaxReasoningTurns
}

func (s *Session) ForkBranch(ctx context.Context, parentBranchID, childBranchID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if parentBranchID == "" || childBranchID == "" {
		return ErrInvalidConfig
	}
	parentEvents, err := s.backend.ListEventsByBranch(ctx, s.id, parentBranchID)
	if err != nil {
		return err
	}
	parentHead := ""
	if len(parentEvents) > 0 {
		parentHead = parentEvents[len(parentEvents)-1].ID
	}
	fork := eventlog.Event{
		ID:            s.nextEventID("branch.fork"),
		SessionID:     s.id,
		TS:            time.Now().UTC(),
		Kind:          eventlog.KindBranchFork,
		BranchID:      childBranchID,
		ParentEventID: parentHead,
		Meta: map[string]any{
			"parent_branch_id": parentBranchID,
		},
	}
	return s.backend.AppendEvent(ctx, fork)
}

func (s *Session) ReplaySession(ctx context.Context) (ReplayResult, error) {
	events, err := s.backend.ListEventsBySession(ctx, s.id)
	if err != nil {
		return ReplayResult{}, err
	}
	state, err := replay.RebuildSession(events)
	if err != nil {
		return ReplayResult{}, err
	}
	branches, err := replay.RebuildBranchStateFromEvents(events)
	if err != nil {
		return ReplayResult{}, err
	}
	heads := make(map[string]string, len(branches))
	for id, b := range branches {
		heads[id] = b.HeadEventID
	}
	return ReplayResult{SessionState: state, Branches: heads}, nil
}

func (s *Session) assembleForBranch(ctx context.Context, branchID string) (assembler.Result, error) {
	branchEvents, err := s.backend.ListEventsByBranch(ctx, s.id, branchID)
	if err != nil {
		return assembler.Result{}, err
	}
	working := make([]assembler.WorkingItem, 0, len(branchEvents))
	for i, e := range branchEvents {
		item := assembler.WorkingItem{
			ID:              e.ID,
			Kind:            e.Kind,
			FrontierOrdinal: i,
			BlobRef:         e.BlobRef,
		}
		switch e.Kind {
		case eventlog.KindTurnUser, eventlog.KindTurnAgent:
			item.Text = readMetaString(e.Meta, "text")
		case eventlog.KindToolResult:
			item.Text = readMetaString(e.Meta, "summary")
			callID := readMetaString(e.Meta, "call_id")
			if callID != "" {
				item.SummaryRef = "summary://tool/" + callID
			}
		}
		working = append(working, item)
	}

	packet, assembleErr := assembler.Assemble(assembler.Request{
		Method:  "POST",
		Path:    "/v1/responses",
		Headers: append([]assembler.Header(nil), s.modelHeaders...),
		Body: assembler.PacketBody{
			SessionID:    s.id,
			BranchHandle: branchID,
			WorkingSet:   working,
		},
	})
	if assembleErr != nil {
		rej := eventlog.Event{
			ID:        s.nextEventID("packet.rejected"),
			SessionID: s.id,
			TS:        time.Now().UTC(),
			Kind:      eventlog.KindPacketRejected,
			BranchID:  branchID,
			Meta: map[string]any{
				"reason": assembleErr.Error(),
			},
		}
		_ = s.backend.AppendEvent(ctx, rej)
		return assembler.Result{}, ErrPacketBudgetRejected
	}

	candidate := eventlog.Event{
		ID:        s.nextEventID("packet.candidate"),
		SessionID: s.id,
		TS:        time.Now().UTC(),
		Kind:      eventlog.KindPacketCandidate,
		BranchID:  branchID,
		Meta: map[string]any{
			"compact_stage":  strconv.Itoa(packet.Stage),
			"envelope_bytes": strconv.Itoa(packet.Measurement.EnvelopeBytes),
		},
	}
	if err := s.backend.AppendEvent(ctx, candidate); err != nil {
		return assembler.Result{}, err
	}
	return packet, nil
}

func (s *Session) executeToolCalls(ctx context.Context, branchID string, calls []model.ToolCall) error {
	if s.toolRunner == nil {
		return ErrNoToolRunnerConfigured
	}

	for _, tc := range calls {
		callID := tc.CallID
		if callID == "" {
			callID = s.nextEventID("tool.call")
		}
		req := tools.Request{
			Tool:      tc.Tool,
			CallID:    callID,
			Args:      tc.Args,
			TimeoutMS: 15000,
		}

		treq := tools.ToolRequestEvent(s.nextEventID("tool.request"), s.id, branchID, time.Now().UTC(), req)
		if err := s.backend.AppendEvent(ctx, treq); err != nil {
			return err
		}

		resp := s.toolRunner.Run(ctx, req)
		if resp.OK {
			rawResult, marshalErr := json.Marshal(resp.Result)
			if marshalErr != nil {
				rawResult = []byte("{}")
			}
			blobRef := fmt.Sprintf("blob://tool/%s/%s", s.id, req.CallID)
			if err := s.backend.AppendBlob(ctx, blobRef, rawResult); err == nil {
				resp.Artifacts = append(resp.Artifacts, tools.Artifact{Name: "result", BlobRef: blobRef})
			}
			tres := tools.ToolResultEvent(s.nextEventID("tool.result"), s.id, branchID, time.Now().UTC(), req, resp)
			if err := s.backend.AppendEvent(ctx, tres); err != nil {
				return err
			}
			continue
		}

		tfail := tools.ToolFailureEvent(s.nextEventID("tool.failure"), s.id, branchID, time.Now().UTC(), req, resp)
		if err := s.backend.AppendEvent(ctx, tfail); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) nextEventID(kind string) string {
	s.counter++
	return fmt.Sprintf("%s.%06d.%s", s.id, s.counter, kind)
}

func readMetaString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	raw, ok := meta[key]
	if !ok {
		return ""
	}
	v, ok := raw.(string)
	if !ok {
		return ""
	}
	return v
}
