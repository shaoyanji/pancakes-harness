package egress

type EligibilityClass string

const (
	ClassPassthrough     EligibilityClass = "passthrough"
	ClassSummaryOnly     EligibilityClass = "summary_only"
	ClassRefOnly         EligibilityClass = "ref_only"
	ClassDropUnlessAsked EligibilityClass = "drop_unless_asked"
	ClassDebugNever      EligibilityClass = "debug_never"
)

type ReasonCode string

const (
	ReasonBranchLocality    ReasonCode = "branch_locality"
	ReasonRecentTurn        ReasonCode = "recent_turn"
	ReasonToolResult        ReasonCode = "tool_result"
	ReasonSummaryCheckpoint ReasonCode = "summary_checkpoint"
	ReasonCheckpointRef     ReasonCode = "checkpoint_ref"
	ReasonGlobalRelevant    ReasonCode = "global_relevant"
	ReasonBudgetFit         ReasonCode = "budget_fit"
	ReasonDebugNever        ReasonCode = "debug_never"
	ReasonNonLocal          ReasonCode = "non_local"
	ReasonSensitiveLocal    ReasonCode = "sensitive_local"
	ReasonRefUnavailable    ReasonCode = "ref_unavailable"
)

type Candidate struct {
	ID              string
	Kind            string
	BranchID        string
	ActiveBranchID  string
	Text            string
	SummaryRef      string
	BlobRef         string
	FrontierOrdinal int

	IsActiveBranch     bool
	IsGlobalRelevant   bool
	IsCurrentUserTurn  bool
	IsLatestToolResult bool
	IsCheckpoint       bool
	IsNearestSummary   bool
	IsSensitiveLocal   bool
}

type Selected struct {
	ID              string
	Kind            string
	Class           EligibilityClass
	Reason          ReasonCode
	Include         bool
	Text            string
	SummaryRef      string
	BlobRef         string
	FrontierOrdinal int
}

type ItemReason struct {
	ID              string           `json:"id"`
	Kind            string           `json:"kind"`
	Reason          ReasonCode       `json:"reason"`
	Class           EligibilityClass `json:"class,omitempty"`
	FrontierOrdinal int              `json:"frontier_ordinal,omitempty"`
}

type ReasonCount struct {
	Reason ReasonCode `json:"reason"`
	Count  int        `json:"count"`
}

type Explanation struct {
	Included                 []ItemReason  `json:"included,omitempty"`
	Excluded                 []ItemReason  `json:"excluded,omitempty"`
	DominantInclusionReasons []ReasonCount `json:"dominant_inclusion_reasons,omitempty"`
	DominantExclusionReasons []ReasonCount `json:"dominant_exclusion_reasons,omitempty"`
	BudgetPressure           bool          `json:"budget_pressure,omitempty"`
}
