// Package evidence implements docs/impl/v1/evidence.md: turning a KU-level
// retrieval candidate into fragment-level evidence by having an LLM pick out
// verbatim supporting spans, then verifying those spans actually occur in
// the KU's source text before trusting them.
package evidence

// EvidenceItem is both Mine's input (one per KU candidate, Content = the
// full KU text, LineStart/LineEnd = the KU's absolute line range) and its
// output (one per surviving fragment, or the original item unchanged when
// mining failed for that candidate — Mined distinguishes the two).
type EvidenceItem struct {
	UnitID       string
	PointID      string
	SourceID     string
	LineStart    int
	LineEnd      int
	Content      string
	PointContent string // the candidate's own KP's abstracted content — the claim mining is meant to find verbatim support for, not itself part of what's mined
	Role         string // "direct" / "supporting" — decides fallback vs. drop on failure
	Origin       string // passthrough (e.g. "rerank" / "kpn_expansion")
	Mined        bool
}

const (
	RoleDirect     = "direct"
	RoleSupporting = "supporting"
)
