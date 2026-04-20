package eventlog

const (
	KindTurnUser          = "turn.user"
	KindTurnAgent         = "turn.agent"
	KindToolRequest       = "tool.request"
	KindToolResult        = "tool.result"
	KindToolFailure       = "tool.failure"
	KindSummaryCheckpoint = "summary.checkpoint"
	KindSummaryRebuild    = "summary.rebuild"
	KindBranchFork        = "branch.fork"
	KindPacketCandidate   = "packet.candidate"
	KindPacketSent        = "packet.sent"
	KindPacketRejected    = "packet.rejected_budget"
	KindResponseReceived  = "response.received"
	KindResponseInvalid   = "response.invalid_schema"
	KindConsultResolved   = "consult.resolved"
	KindConsultUnresolved = "consult.unresolved"

	// New event kinds for v0.3.0 harness upgrades.
	KindRecoveryAttempt = "recovery.attempt"   // self-healing loop recovery attempt
	KindRecoveryFallback = "recovery.fallback" // self-healing loop model fallback
	KindContextCompact  = "context.compact"    // context compaction event
	KindDreamResult     = "dream.result"        // dream daemon reflective pass result
	KindAuditDecision   = "audit.decision"      // self-audit termination decision
	KindPreprocessExtract = "preprocess.extraction" // fast model enrichment result
	KindPreprocessRoute   = "preprocess.routing"    // strong model routing decision
	KindPreprocessEnvelope = "preprocess.envelope"  // combined two-tier preprocessing record
)
