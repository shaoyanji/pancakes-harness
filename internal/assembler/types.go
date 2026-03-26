package assembler

import "errors"

const (
	MaxEnvelopeBytes  = 14336
	SafetyMarginBytes = 768

	// LargeTextInlineThresholdBytes controls when text is treated as large local data.
	// When a blob reference exists, large text is never shipped inline.
	LargeTextInlineThresholdBytes = 1024
	ToolResultExcerptBytes        = 256
)

var ErrPacketRejectedBudget = errors.New("packet rejected by budget")

type Header struct {
	Name  string
	Value string
}

type Constraint struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type WorkingItem struct {
	ID                 string `json:"id"`
	Kind               string `json:"kind"`
	Text               string `json:"text,omitempty"`
	SummaryRef         string `json:"summary_ref,omitempty"`
	BlobRef            string `json:"blob_ref,omitempty"`
	FrontierOrdinal    int    `json:"frontier_ordinal,omitempty"`
	Provenance         string `json:"provenance,omitempty"`
	ProvenanceRequired bool   `json:"-"`
}

type PacketBody struct {
	SessionID            string        `json:"session_id"`
	BranchHandle         string        `json:"branch_handle"`
	WorkingSet           []WorkingItem `json:"working_set"`
	ExternalContext      string        `json:"external_context,omitempty"`
	Frontier             []string      `json:"frontier,omitempty"`
	Constraints          []Constraint  `json:"constraints,omitempty"`
	Debug                []string      `json:"debug,omitempty"`
	Provenance           []string      `json:"provenance,omitempty"`
	CheckpointSummaryRef string        `json:"checkpoint_summary_ref,omitempty"`
	CompactStage         int           `json:"compact_stage"`
}

type Request struct {
	Method  string
	Path    string
	Headers []Header
	Body    PacketBody
}

type Measurement struct {
	RequestLineBytes int
	HeaderBytes      int
	BodyBudgetBytes  int
	BodyBytes        int
	EnvelopeBytes    int
}

type Result struct {
	BodyJSON              []byte
	Stage                 int
	Measurement           Measurement
	Body                  PacketBody
	ExternalContextStatus string
	ExternalContextBytes  int
}
