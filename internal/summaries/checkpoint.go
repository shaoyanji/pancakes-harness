package summaries

import "errors"

var (
	ErrInvalidCheckpoint = errors.New("invalid summary checkpoint")
)

type CoveredRange struct {
	StartEventID string
	EndEventID   string
}

type SummaryCheckpoint struct {
	SummaryID        string
	BranchID         string
	BasisEventID     string
	CoveredRange     CoveredRange
	TextRef          string
	BlobRef          string
	ByteEstimate     int
	TokenEstimate    int
	FreshnessVersion int
}

func (c SummaryCheckpoint) Validate() error {
	if c.SummaryID == "" || c.BranchID == "" || c.BasisEventID == "" {
		return ErrInvalidCheckpoint
	}
	if c.CoveredRange.StartEventID == "" || c.CoveredRange.EndEventID == "" {
		return ErrInvalidCheckpoint
	}
	if c.TextRef == "" && c.BlobRef == "" {
		return ErrInvalidCheckpoint
	}
	if c.ByteEstimate < 0 || c.TokenEstimate < 0 || c.FreshnessVersion < 0 {
		return ErrInvalidCheckpoint
	}
	return nil
}
