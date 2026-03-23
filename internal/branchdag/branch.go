package branchdag

import "errors"

var (
	ErrInvalidBranch    = errors.New("invalid branch")
	ErrBranchExists     = errors.New("branch already exists")
	ErrBranchNotFound   = errors.New("branch not found")
	ErrInvalidEventID   = errors.New("invalid event id")
	ErrInvalidParentRef = errors.New("invalid parent branch reference")
)

type DirtyRange struct {
	StartEventID string
	EndEventID   string
}

type Branch struct {
	BranchID         string
	ParentBranchID   string
	ForkEventID      string
	HeadEventID      string
	BaseSummaryID    string
	DirtyRanges      []DirtyRange
	Score            float64
	MaterializedHint string
}

func (b Branch) Validate() error {
	if b.BranchID == "" {
		return ErrInvalidBranch
	}
	for _, r := range b.DirtyRanges {
		if r.StartEventID == "" || r.EndEventID == "" {
			return ErrInvalidBranch
		}
	}
	return nil
}

func cloneBranch(in Branch) Branch {
	out := in
	if in.DirtyRanges != nil {
		out.DirtyRanges = append([]DirtyRange(nil), in.DirtyRanges...)
	}
	return out
}

func AppendToBranch(b Branch, eventID string) (Branch, error) {
	if eventID == "" {
		return Branch{}, ErrInvalidEventID
	}
	if err := b.Validate(); err != nil {
		return Branch{}, err
	}

	out := cloneBranch(b)
	if len(out.DirtyRanges) == 0 {
		out.DirtyRanges = append(out.DirtyRanges, DirtyRange{
			StartEventID: eventID,
			EndEventID:   eventID,
		})
	} else {
		last := len(out.DirtyRanges) - 1
		out.DirtyRanges[last].EndEventID = eventID
	}
	out.HeadEventID = eventID
	return out, nil
}
