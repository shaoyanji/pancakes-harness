package branchdag

import "sort"

type Graph struct {
	branches map[string]Branch
}

func NewGraph() *Graph {
	return &Graph{
		branches: make(map[string]Branch),
	}
}

func (g *Graph) CreateBranch(branch Branch) (Branch, error) {
	if err := branch.Validate(); err != nil {
		return Branch{}, err
	}
	if _, exists := g.branches[branch.BranchID]; exists {
		return Branch{}, ErrBranchExists
	}
	stored := cloneBranch(branch)
	g.branches[branch.BranchID] = stored
	return cloneBranch(stored), nil
}

func (g *Graph) ForkBranch(childBranchID, parentBranchID, forkEventID string) (Branch, error) {
	if childBranchID == "" || parentBranchID == "" {
		return Branch{}, ErrInvalidBranch
	}
	if _, exists := g.branches[childBranchID]; exists {
		return Branch{}, ErrBranchExists
	}
	parent, ok := g.branches[parentBranchID]
	if !ok {
		return Branch{}, ErrInvalidParentRef
	}

	child := Branch{
		BranchID:       childBranchID,
		ParentBranchID: parentBranchID,
		ForkEventID:    forkEventID,
		HeadEventID:    parent.HeadEventID,
		BaseSummaryID:  parent.BaseSummaryID,
		DirtyRanges:    append([]DirtyRange(nil), parent.DirtyRanges...),
		Score:          parent.Score,
	}
	g.branches[childBranchID] = child
	return cloneBranch(child), nil
}

func (g *Graph) AppendEvent(branchID, eventID string) (Branch, error) {
	branch, ok := g.branches[branchID]
	if !ok {
		return Branch{}, ErrBranchNotFound
	}
	updated, err := AppendToBranch(branch, eventID)
	if err != nil {
		return Branch{}, err
	}
	g.branches[branchID] = updated
	return cloneBranch(updated), nil
}

func (g *Graph) SetBaseSummary(branchID, summaryID string) (Branch, error) {
	branch, ok := g.branches[branchID]
	if !ok {
		return Branch{}, ErrBranchNotFound
	}
	branch.BaseSummaryID = summaryID
	g.branches[branchID] = branch
	return cloneBranch(branch), nil
}

func (g *Graph) RebaseOnSummary(branchID, summaryID, basisEventID string) (Branch, error) {
	branch, ok := g.branches[branchID]
	if !ok {
		return Branch{}, ErrBranchNotFound
	}
	branch.BaseSummaryID = summaryID
	if basisEventID != "" {
		branch.HeadEventID = basisEventID
	}
	branch.DirtyRanges = nil
	g.branches[branchID] = branch
	return cloneBranch(branch), nil
}

func (g *Graph) GetBranch(branchID string) (Branch, error) {
	branch, ok := g.branches[branchID]
	if !ok {
		return Branch{}, ErrBranchNotFound
	}
	return cloneBranch(branch), nil
}

func (g *Graph) ListBranches() []Branch {
	keys := make([]string, 0, len(g.branches))
	for id := range g.branches {
		keys = append(keys, id)
	}
	sort.Strings(keys)

	out := make([]Branch, 0, len(keys))
	for _, k := range keys {
		out = append(out, cloneBranch(g.branches[k]))
	}
	return out
}
