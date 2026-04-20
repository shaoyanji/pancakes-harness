package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"pancakes-harness/internal/assembler"
	"pancakes-harness/internal/audit"
	"pancakes-harness/internal/backend"
	"pancakes-harness/internal/compactor"
	"pancakes-harness/internal/egress"
	"pancakes-harness/internal/eventlog"
	"pancakes-harness/internal/memory"
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

	// New v0.3.0 fields
	MemoryManager *memory.Manager
	AuditConfig   audit.Config

	// v0.3.1 Gemini compaction
	Compactor        compactor.Compactor
	CompactionSchedule compactor.ScheduleConfig
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

	// v0.3.0 fields
	memoryManager *memory.Manager
	auditConfig   audit.Config

	// v0.3.1 Gemini compaction
	compactor compactor.Compactor
	scheduler *compactor.Scheduler

	mu      sync.Mutex
	counter int
}

type TurnResult struct {
	SessionID            string
	BranchID             string
	Answer               string
	Decision             string
	PacketEnvelopeBytes  int
	Selected             []egress.Selected
	SelectionExplanation *egress.Explanation
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

	// Initialize compaction scheduler if compactor is configured
	var sched *compactor.Scheduler
	if cfg.Compactor != nil {
		sched = compactor.NewScheduler(cfg.CompactionSchedule)
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
		memoryManager:     cfg.MemoryManager,
		auditConfig:       cfg.AuditConfig,
		compactor:         cfg.Compactor,
		scheduler:         sched,
	}

	events, err := s.backendListEventsBySession(context.Background(), s.id)
	if err != nil {
		return nil, err
	}
	s.counter = len(events)

	// Bootstrap the content search index with existing events
	if s.memoryManager != nil {
		s.memoryManager.BootstrapSearchIndex(events)
	}

	return s, nil
}

func (s *Session) HandleUserTurn(ctx context.Context, branchID, text string) (TurnResult, error) {
	return s.handleUserTurn(ctx, branchID, text, "")
}

func (s *Session) HandleUserTurnWithExternalContext(ctx context.Context, branchID, text, externalContext string) (TurnResult, error) {
	return s.handleUserTurn(ctx, branchID, text, externalContext)
}

func (s *Session) handleUserTurn(ctx context.Context, branchID, text, externalContext string) (TurnResult, error) {
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

	// Index the event in the memory layer (layer 1)
	if s.memoryManager != nil {
		s.memoryManager.IndexEvent(userEvent)
	}

	// Initialize audit tracker for this turn
	consultID := s.nextEventID("consult")
	var auditTracker *audit.Tracker
	if s.metrics != nil {
		auditTracker = audit.NewTracker(s.auditConfig, consultID, s.id, branchID)
	}

	var lastPacketBytes int
	var lastSelected []egress.Selected
	var lastExplanation egress.Explanation
	for i := 0; i < s.maxReasoningTurns; i++ {
		assembleStarted := time.Now()
		packet, selected, explanation, err := s.assembleForBranch(ctx, branchID, externalContext)
		if err != nil {
			return TurnResult{}, err
		}
		s.observeLatency("packet_assembly_ms", time.Since(assembleStarted))
		lastPacketBytes = packet.Measurement.EnvelopeBytes
		lastSelected = append(lastSelected[:0], selected...)
		lastExplanation = explanation
		s.observeEnvelopeBytes(packet.Measurement.EnvelopeBytes)
		s.observeBodyBytes(len(packet.BodyJSON))
		s.incCompactionStage(packet.Stage)

		// Report budget pressure to compaction scheduler
		if s.scheduler != nil {
			budgetRatio := float64(packet.Measurement.EnvelopeBytes) / float64(assembler.MaxEnvelopeBytes)
			s.scheduler.RecordBudgetPressure(budgetRatio)
		}

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
				"compact_stage":           strconv.Itoa(packet.Stage),
				"envelope_bytes":          strconv.Itoa(packet.Measurement.EnvelopeBytes),
				"external_context_status": packet.ExternalContextStatus,
				"external_context_bytes":  strconv.Itoa(packet.ExternalContextBytes),
			},
		}
		if err := s.backendAppendEvent(ctx, packetSent); err != nil {
			return TurnResult{}, err
		}

		modelStarted := time.Now()
		callResult, callErr := model.ExecuteAndPersist(ctx, s.backend, s.modelAdapter, callReq, modelEventID, time.Now().UTC())
		s.observeLatency("model_call_ms", time.Since(modelStarted))
		if callErr != nil {
			// Record audit event with error context
			if auditTracker != nil {
				auditTracker.RecordTurn(ctx, 0, 0, s.appendAuditEvent)
			}
			return TurnResult{
				SessionID:           s.id,
				BranchID:            branchID,
				PacketEnvelopeBytes: lastPacketBytes,
			}, callErr
		}

		// Record audit event with token usage (estimated from envelope bytes)
		if auditTracker != nil {
			estimatedTokens := packet.Measurement.EnvelopeBytes / 4 // rough estimate
			auditResult := auditTracker.RecordTurn(ctx, estimatedTokens, 0, s.appendAuditEvent)
			// Check for early termination
			if auditTracker.ShouldTerminate() && auditResult.Decision == audit.DecisionComplete {
				// Return what we have if audit says complete
				if callResult.Response.Decision == "answer" {
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
						SessionID:            s.id,
						BranchID:             branchID,
						Answer:               callResult.Response.Answer,
						Decision:             callResult.Response.Decision,
						PacketEnvelopeBytes:  lastPacketBytes,
						Selected:             append([]egress.Selected(nil), lastSelected...),
						SelectionExplanation: cloneExplanation(lastExplanation),
					}, nil
				}
			}
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
			// Index the agent event
			if s.memoryManager != nil {
				s.memoryManager.IndexEvent(agent)
			}

			// Record turn and maybe trigger compaction
			s.recordTurnAndMaybeCompact(ctx, branchID)

			return TurnResult{
				SessionID:            s.id,
				BranchID:             branchID,
				Answer:               callResult.Response.Answer,
				Decision:             callResult.Response.Decision,
				PacketEnvelopeBytes:  lastPacketBytes,
				Selected:             append([]egress.Selected(nil), lastSelected...),
				SelectionExplanation: cloneExplanation(lastExplanation),
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

func (s *Session) assembleForBranch(ctx context.Context, branchID, externalContext string) (assembler.Result, []egress.Selected, egress.Explanation, error) {
	sessionEvents, err := s.backendListEventsBySession(ctx, s.id)
	if err != nil {
		return assembler.Result{}, nil, egress.Explanation{}, err
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
	latestUserQuery := ""
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
			if e.Kind == eventlog.KindTurnUser && isActiveBranch && e.ID == latestUserID {
				latestUserQuery = cand.Text
			}
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

	// Content search enrichment: mark search-relevant events as global relevant
	// Skip single-word or very short queries — they produce noise
	if s.memoryManager != nil && len(strings.Fields(latestUserQuery)) >= 2 {
		searchResults := s.memoryManager.SearchEvents(latestUserQuery, "", 50)
		resultSet := make(map[string]float64, len(searchResults))
		for _, r := range searchResults {
			resultSet[r.EventID] = r.Score
		}
		for i := range candidates {
			if _, ok := resultSet[candidates[i].ID]; ok {
				candidates[i].IsGlobalRelevant = true
			}
		}
	}

	selected, explanation := egress.SelectWithExplanation(candidates)
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
			SessionID:       s.id,
			BranchHandle:    branchID,
			WorkingSet:      working,
			ExternalContext: externalContext,
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
		return assembler.Result{}, nil, egress.Explanation{}, ErrPacketBudgetRejected
	}
	explanation.BudgetPressure = packet.Stage > 0 || packet.ExternalContextStatus == "dropped_budget"
	s.observeSelectionExplanation(explanation)

	candidate := eventlog.Event{
		ID:        s.nextEventID("packet.candidate"),
		SessionID: s.id,
		TS:        time.Now().UTC(),
		Kind:      eventlog.KindPacketCandidate,
		BranchID:  branchID,
		Meta: map[string]any{
			"compact_stage":           strconv.Itoa(packet.Stage),
			"envelope_bytes":          strconv.Itoa(packet.Measurement.EnvelopeBytes),
			"external_context_status": packet.ExternalContextStatus,
			"external_context_bytes":  strconv.Itoa(packet.ExternalContextBytes),
		},
	}
	if err := s.backendAppendEvent(ctx, candidate); err != nil {
		return assembler.Result{}, nil, egress.Explanation{}, err
	}
	cloned := cloneExplanation(explanation)
	if cloned == nil {
		return packet, append([]egress.Selected(nil), selected...), egress.Explanation{}, nil
	}
	return packet, append([]egress.Selected(nil), selected...), *cloned, nil
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

func (s *Session) appendAuditEvent(ev eventlog.Event) error {
	if s.metrics == nil {
		return nil
	}
	return s.backendAppendEvent(context.Background(), ev)
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

func (s *Session) observeSelectionExplanation(explanation egress.Explanation) {
	if s.metrics == nil {
		return
	}
	for _, reason := range explanation.DominantInclusionReasons {
		s.metrics.IncSelectorInclusionReason(string(reason.Reason), reason.Count)
	}
	for _, reason := range explanation.DominantExclusionReasons {
		s.metrics.IncSelectorExclusionReason(string(reason.Reason), reason.Count)
	}
	if explanation.BudgetPressure {
		s.metrics.IncSelectorBudgetPressure()
	}
}

// recordTurnAndMaybeCompact records the completed turn in the scheduler
// and fires compaction if conditions are met.
func (s *Session) recordTurnAndMaybeCompact(ctx context.Context, branchID string) {
	if s.scheduler == nil || s.compactor == nil {
		return
	}

	s.scheduler.RecordTurn()

	// Update event count for the scheduler
	events, err := s.backendListEventsByBranch(ctx, s.id, branchID)
	if err == nil {
		s.scheduler.SetEventCount(len(events))
	}

	should, reason := s.scheduler.ShouldCompact()
	if !should {
		return
	}

	s.runCompaction(ctx, branchID, events, reason)
}

// runCompaction executes a compaction pass and records events to the spine.
func (s *Session) runCompaction(ctx context.Context, branchID string, events []eventlog.Event, reason string) {
	now := time.Now().UTC()

	// Write compaction.request event
	requestEvent := eventlog.Event{
		ID:        s.nextEventID("compaction.request"),
		SessionID: s.id,
		TS:        now,
		Kind:      eventlog.KindCompactionRequest,
		BranchID:  branchID,
		Meta: map[string]any{
			"trigger_reason": reason,
			"event_count":    len(events),
			"compactor":      s.compactor.Name(),
		},
	}
	if err := s.backendAppendEvent(ctx, requestEvent); err != nil {
		// Non-fatal — compaction is best-effort
		s.observeLatency("compaction_error_ms", 0)
		return
	}

	// Run the compactor
	compactStart := time.Now()
	compactReq := compactor.CompactRequest{
		SessionID: s.id,
		BranchID:  branchID,
		Events:    events,
	}
	resp, err := s.compactor.CompactContext(ctx, compactReq)
	compactLatency := time.Since(compactStart)

	if err != nil {
		// Write compaction.failure event
		failureEvent := eventlog.Event{
			ID:        s.nextEventID("compaction.failure"),
			SessionID: s.id,
			TS:        time.Now().UTC(),
			Kind:      eventlog.KindCompactionFailure,
			BranchID:  branchID,
			Meta: map[string]any{
				"trigger_reason": reason,
				"error":          err.Error(),
			},
		}
		_ = s.backendAppendEvent(ctx, failureEvent)
		s.scheduler.MarkCompactionFailed()
		s.observeLatency("compaction_ms", compactLatency)
		return
	}

	// Persist AST blob
	blobRef := resp.Checkpoint.BlobRef
	if blobErr := s.backendAppendBlob(ctx, blobRef, resp.RawAST); blobErr != nil {
		// Non-fatal — AST not persisted, but we still have the checkpoint
	}

	// Write ast.persisted event
	astEvent := eventlog.Event{
		ID:        s.nextEventID("ast.persisted"),
		SessionID: s.id,
		TS:        time.Now().UTC(),
		Kind:      eventlog.KindASTPersisted,
		BranchID:  branchID,
		Meta: map[string]any{
			"blob_ref":       blobRef,
			"summary_id":     resp.Checkpoint.SummaryID,
			"input_events":   resp.Metrics.InputEvents,
			"output_bytes":   resp.Metrics.OutputBytes,
			"chunk_count":    resp.Metrics.ChunkCount,
			"merge_pass":     resp.Metrics.MergePass,
			"compression_pct": fmt.Sprintf("%.1f", resp.Metrics.CompressionPct),
		},
	}
	_ = s.backendAppendEvent(ctx, astEvent)

	// Write compaction.complete event
	completeEvent := eventlog.Event{
		ID:        s.nextEventID("compaction.complete"),
		SessionID: s.id,
		TS:        time.Now().UTC(),
		Kind:      eventlog.KindCompactionComplete,
		BranchID:  branchID,
		Meta: map[string]any{
			"trigger_reason":  reason,
			"summary_id":      resp.Checkpoint.SummaryID,
			"blob_ref":        blobRef,
			"input_events":    resp.Metrics.InputEvents,
			"output_bytes":    resp.Metrics.OutputBytes,
			"compression_pct": fmt.Sprintf("%.1f", resp.Metrics.CompressionPct),
			"input_tokens":    resp.Metrics.InputTokens,
			"output_tokens":   resp.Metrics.OutputTokens,
			"total_latency_ms": compactLatency.Milliseconds(),
			"chunk_count":     resp.Metrics.ChunkCount,
			"merge_pass":      resp.Metrics.MergePass,
			"leaves_in_ast":   len(resp.AST.Leaves),
		},
	}
	_ = s.backendAppendEvent(ctx, completeEvent)

	s.scheduler.MarkCompacted()
	s.observeLatency("compaction_ms", compactLatency)
}

func cloneExplanation(in egress.Explanation) *egress.Explanation {
	out := egress.Explanation{
		BudgetPressure:           in.BudgetPressure,
		Included:                 append([]egress.ItemReason(nil), in.Included...),
		Excluded:                 append([]egress.ItemReason(nil), in.Excluded...),
		DominantInclusionReasons: append([]egress.ReasonCount(nil), in.DominantInclusionReasons...),
		DominantExclusionReasons: append([]egress.ReasonCount(nil), in.DominantExclusionReasons...),
	}
	return &out
}
