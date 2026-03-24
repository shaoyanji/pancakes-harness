package egress

type EligibilityClass string

const (
	ClassPassthrough     EligibilityClass = "passthrough"
	ClassSummaryOnly     EligibilityClass = "summary_only"
	ClassRefOnly         EligibilityClass = "ref_only"
	ClassDropUnlessAsked EligibilityClass = "drop_unless_asked"
	ClassDebugNever      EligibilityClass = "debug_never"
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
	Include         bool
	Text            string
	SummaryRef      string
	BlobRef         string
	FrontierOrdinal int
}
