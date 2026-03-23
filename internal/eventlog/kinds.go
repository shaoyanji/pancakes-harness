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
)
