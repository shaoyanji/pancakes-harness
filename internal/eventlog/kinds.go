package eventlog

const (
	KindTurnUser          = "turn.user"
	KindTurnAgent         = "turn.agent"
	KindSummaryCheckpoint = "summary.checkpoint"
	KindSummaryRebuild    = "summary.rebuild"
	KindBranchFork        = "branch.fork"
	KindPacketCandidate   = "packet.candidate"
	KindPacketSent        = "packet.sent"
	KindPacketRejected    = "packet.rejected_budget"
)
