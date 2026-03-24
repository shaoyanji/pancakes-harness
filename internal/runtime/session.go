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
	"pancakes-harness/internal/egress"
	"pancakes-harness/internal/eventlog"
	"pancakes-harness/internal/metrics"
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
	Metrics           *metrics.Registry
}

type Session struct {
	id                string
	defaultBranchID   string
	backend           backend.Backend
	modelAdapter      model.Adapter
	toolRunner        *tools.Runner
	modelHeaders      []assembler.Header
	maxReasoningTurns int
	metrics           *metrics.Registry

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
		metrics:           cfg.Metrics,
	}

	events, err := s.backendListEventsBySession(context.Background(), s.id)
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
	if err := s.backendAppendEvent(ctx, userEvent); err != nil {
		return TurnResult{}, err
	}

	var lastPacketBytes int
	for i := 0; i < s.maxReasoningTurns; i++ {
		assembleStarted := time.Now()
		packet, err := s.assembleForBranch(ctx, branchID)
		if err != nil {
			return TurnResult{}, err
		}
		s.observeLatency("packet_assembly_ms", time.Since(assembleStarted))
		lastPacketBytes = packet.Measurement.EnvelopeBytes
		s.observeEnvelopeBytes(packet.Measurement.EnvelopeBytes)
		s.observeBodyBytes(len(packet.BodyJSON))
		s.incCompactionStage(packet.Stage)

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
		if err := s.backendAppendEvent(ctx, packetSent); err != nil {
			return TurnResult{}, err
		}

		modelStarted := time.Now()
		callResult, callErr := model.ExecuteAndPersist(ctx, s.backend, s.modelAdapter, callReq, modelEventID, time.Now().UTC())
		s.observeLatency("model_call_ms", time.Since(modelStarted))
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
			if err := s.backendAppendEvent(ctx, agent); err != nil {
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
	parentEvents, err := s.backendListEventsByBranch(ctx, s.id, parentBranchID)
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
	return s.backendAppendEvent(ctx, fork)
}

func (s *Session) ReplaySession(ctx context.Context) (ReplayResult, error) {
	events, err := s.backendListEventsBySession(ctx, s.id)
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
	sessionEvents, err := s.backendListEventsBySession(ctx, s.id)
	if err != nil {
		return assembler.Result{}, err
	}

	latestUserID := ""
	latestToolResultID := ""
	nearestCheckpointID := ""
	for i := len(sessionEvents) - 1; i >= 0; i-- {
		e := sessionEvents[i]
		if e.BranchID != branchID {
			continue
		}
		if latestUserID == "" && e.Kind == eventlog.KindTurnUser {
			latestUserID = e.ID
		}
		if latestToolResultID == "" && e.Kind == eventlog.KindToolResult {
			latestToolResultID = e.ID
		}
		if nearestCheckpointID == "" && e.Kind == eventlog.KindSummaryCheckpoint {
			nearestCheckpointID = e.ID
		}
		if latestUserID != "" && latestToolResultID != "" && nearestCheckpointID != "" {
			break
		}
	}

	candidates := make([]egress.Candidate, 0, len(sessionEvents))
	for i, e := range sessionEvents {
		isActiveBranch := e.BranchID == branchID
		cand := egress.Candidate{
			ID:                 e.ID,
			Kind:               e.Kind,
			BranchID:           e.BranchID,
			ActiveBranchID:     branchID,
			BlobRef:            e.BlobRef,
			FrontierOrdinal:    i,
			IsActiveBranch:     isActiveBranch,
			IsCurrentUserTurn:  isActiveBranch && e.ID == latestUserID,
			IsLatestToolResult: isActiveBranch && e.ID == latestToolResultID,
			IsCheckpoint:       isActiveBranch && e.Kind == eventlog.KindSummaryCheckpoint,
			IsNearestSummary:   isActiveBranch && e.ID == nearestCheckpointID,
		}
		switch e.Kind {
		case eventlog.KindTurnUser, eventlog.KindTurnAgent:
			cand.Text = readMetaString(e.Meta, "text")
		case eventlog.KindToolResult:
			cand.Text = readMetaString(e.Meta, "summary")
			callID := readMetaString(e.Meta, "call_id")
			if callID != "" {
				cand.SummaryRef = "summary://tool/" + callID
			}
		case eventlog.KindSummaryCheckpoint:
			summaryID := readMetaString(e.Meta, "summary_id")
			if summaryID != "" {
				cand.SummaryRef = "summary://checkpoint/" + summaryID
			}
		}
		if e.Kind == eventlog.KindToolRequest || e.Kind == eventlog.KindToolFailure {
			cand.IsSensitiveLocal = true
		}
		if readMetaString(e.Meta, "global_relevant") == "true" {
			cand.IsGlobalRelevant = true
		}
		candidates = append(candidates, cand)
	}

	selected := egress.Select(candidates)
	working := make([]assembler.WorkingItem, 0, len(selected))
	for _, sel := range selected {
		if !sel.Include {
			continue
		}
		item := assembler.WorkingItem{
			ID:              sel.ID,
			Kind:            sel.Kind,
			Text:            sel.Text,
			SummaryRef:      sel.SummaryRef,
			BlobRef:         sel.BlobRef,
			FrontierOrdinal: sel.FrontierOrdinal,
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
		s.incPacketBudgetRejection()
		_ = s.backendAppendEvent(ctx, rej)
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
	if err := s.backendAppendEvent(ctx, candidate); err != nil {
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
		if err := s.backendAppendEvent(ctx, treq); err != nil {
			return err
		}

		toolStarted := time.Now()
		resp := s.toolRunner.Run(ctx, req)
		s.observeLatency("tool_call_ms", time.Since(toolStarted))
		if resp.OK {
			rawResult, marshalErr := json.Marshal(resp.Result)
			if marshalErr != nil {
				rawResult = []byte("{}")
			}
			blobRef := fmt.Sprintf("blob://tool/%s/%s", s.id, req.CallID)
			if err := s.backendAppendBlob(ctx, blobRef, rawResult); err == nil {
				resp.Artifacts = append(resp.Artifacts, tools.Artifact{Name: "result", BlobRef: blobRef})
			}
			tres := tools.ToolResultEvent(s.nextEventID("tool.result"), s.id, branchID, time.Now().UTC(), req, resp)
			if err := s.backendAppendEvent(ctx, tres); err != nil {
				return err
			}
			continue
		}

		tfail := tools.ToolFailureEvent(s.nextEventID("tool.failure"), s.id, branchID, time.Now().UTC(), req, resp)
		if err := s.backendAppendEvent(ctx, tfail); err != nil {
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

func (s *Session) backendAppendEvent(ctx context.Context, e eventlog.Event) error {
	started := time.Now()
	err := s.backend.AppendEvent(ctx, e)
	s.observeBackend("append_event_ms", time.Since(started))
	return err
}

func (s *Session) backendAppendBlob(ctx context.Context, ref string, payload []byte) error {
	started := time.Now()
	err := s.backend.AppendBlob(ctx, ref, payload)
	s.observeBackend("append_blob_ms", time.Since(started))
	return err
}

func (s *Session) backendListEventsBySession(ctx context.Context, sessionID string) ([]eventlog.Event, error) {
	started := time.Now()
	events, err := s.backend.ListEventsBySession(ctx, sessionID)
	s.observeBackend("list_events_by_session_ms", time.Since(started))
	return events, err
}

func (s *Session) backendListEventsByBranch(ctx context.Context, sessionID, branchID string) ([]eventlog.Event, error) {
	started := time.Now()
	events, err := s.backend.ListEventsByBranch(ctx, sessionID, branchID)
	s.observeBackend("list_events_by_branch_ms", time.Since(started))
	return events, err
}

func (s *Session) observeLatency(name string, d time.Duration) {
	if s.metrics != nil {
		s.metrics.ObserveLatency(name, d)
	}
}

func (s *Session) observeBackend(name string, d time.Duration) {
	if s.metrics != nil {
		s.metrics.ObserveBackendOp(name, d)
	}
}

func (s *Session) observeEnvelopeBytes(n int) {
	if s.metrics != nil {
		s.metrics.ObserveEnvelopeBytes(n)
	}
}

func (s *Session) observeBodyBytes(n int) {
	if s.metrics != nil {
		s.metrics.ObserveBodyBytes(n)
	}
}

func (s *Session) incCompactionStage(stage int) {
	if s.metrics != nil {
		s.metrics.IncCompactionStage(stage)
	}
}

func (s *Session) incPacketBudgetRejection() {
	if s.metrics != nil {
		s.metrics.IncPacketBudgetRejection()
	}
}
