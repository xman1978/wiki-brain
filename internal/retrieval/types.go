package retrieval

import "encoding/json"

type QueryContext struct {
	Question   string
	Subject    string
	Intent     string
	Audience   string
	Constraint string
	// ForceFull skips the activation fast path and forces the full MVP
	// pipeline — POST /retrieval's force_full request field
	// (docs/impl/v1/retrieval.md 步骤 7), for debugging/comparison evals.
	ForceFull bool
}

type EvidenceSet struct {
	Question       string             `json:"question"`
	Subject        string             `json:"subject,omitempty"`
	Intent         string             `json:"intent,omitempty"`
	Audience       string             `json:"audience,omitempty"`
	Constraint     string             `json:"constraint,omitempty"`
	Path           string             `json:"path"`
	PathType       string             `json:"path_type"`
	ActivationHits []ActivationHit    `json:"activation_hits"`
	DirectEvidence []Evidence         `json:"direct_evidence"`
	Supporting     []Evidence         `json:"supporting"`
	Conflicts      []ConflictEvidence `json:"conflicts,omitempty"`

	// GapReason and FilteredEvidence are slow-path-only diagnostics for
	// knowledge gaps (docs/impl/v1/retrieval.md 步骤 6, docs/impl/v1/study.md
	// "knowledge_gaps 表扩展"). GapReason is set to no_candidates when RRF
	// merge produces zero candidates, or judge_filtered when candidates
	// existed but rerank judged all of them irrelevant (direct+supporting
	// still empty after sufficiency check); left empty otherwise.
	// FilteredEvidence holds the candidates rerank judged "irrelevant" —
	// role=RoleIrrelevant, FactID left empty (not mined, not citable) —
	// kept regardless of GapReason so a non-gap answer can still show what
	// was considered and excluded.
	GapReason        string     `json:"gap_reason,omitempty"`
	FilteredEvidence []Evidence `json:"filtered_evidence,omitempty"`

	// Wiki direct-answer fields (docs/impl/v1/wiki.md 步骤 4). Only meaningful
	// when PathType==PathTypeWiki; this is the entire persisted
	// evidence_snapshot shape for that path ("{wiki_page_id, cited_point_ids}"
	// — no DirectEvidence/Supporting for wiki answers).
	WikiPageID    string   `json:"wiki_page_id,omitempty"`
	CitedPointIDs []string `json:"cited_point_ids,omitempty"`
	// WikiAnswerContent is the final answer text Retrieval's Wiki layer
	// already generated (docs/impl/v1/wiki.md 步骤 4's answer_wiki.md call) —
	// a side channel Answer consumes directly without its own LLM call.
	// Deliberately excluded from evidence_snapshot (json:"-"): the doc's
	// persisted shape has no content field, only wiki_page_id/cited_point_ids.
	WikiAnswerContent string `json:"-"`
}

// PathType values (docs/impl/v1/retrieval.md, docs/impl/v1/trace.md).
const (
	PathTypeFast = "fast"
	PathTypeFull = "full"
	PathTypeWiki = "wiki"
)

// ActivationHit is a link that matched during the activation layer
// (docs/impl/v1/activation.md 步骤 2 Match()), carried through to Trace so it
// can grade activation_success/activation_failure per link
// (docs/impl/v1/trace.md 步骤 3).
type ActivationHit struct {
	LinkID     string  `json:"link_id"`
	PointID    string  `json:"point_id"`
	MatchScore float64 `json:"match_score"`
}

type Evidence struct {
	FactID      string          `json:"fact_id"`
	CandidateID string          `json:"-"`
	UnitID      string          `json:"unit_id"`
	PointID     string          `json:"point_id"`
	Content     string          `json:"content"`
	SourceRef   json.RawMessage `json:"source_ref"`
	Role        string          `json:"role"`
	Origin      string          `json:"origin"`
	// rerank（Rerank 直接分类产出，direct 恒为此值）/ kpn_expansion（KPN 邻居扩展补充的 supporting）
	// 供 Trace 计算 KPN 引用采纳率（study.md summary.kpn_citation_rate）
	Mined bool `json:"mined"`
	// 该证据是否为挖掘出的片段，false=整段回退（证据挖掘未实现前恒为 false，见 docs/impl/v1/evidence.md）
}

const (
	OriginRerank       = "rerank"
	OriginKPNExpansion = "kpn_expansion"
)

// RoleIrrelevant marks FilteredEvidence entries — candidates the rerank
// judge classified as irrelevant rather than direct/supporting.
const RoleIrrelevant = "irrelevant"

// Gap reason values for EvidenceSet.GapReason (docs/impl/v1/retrieval.md 步骤 6).
const (
	GapReasonNoCandidates  = "no_candidates"
	GapReasonJudgeFiltered = "judge_filtered"
)

type ProgressEvent struct {
	Phase    string `json:"phase"`
	Status   string `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Duration int64  `json:"duration_ms,omitempty"`
}

type ProgressFunc func(evt ProgressEvent)

type ConflictEvidence struct {
	UnitID      string          `json:"unit_id"`
	PointID     string          `json:"point_id"`
	Content     string          `json:"content"`
	SourceRef   json.RawMessage `json:"source_ref"`
	SourceTitle string          `json:"source_title"`
}

type SourceRef struct {
	SourceID  string `json:"source_id"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
}

type candidate struct {
	candidateID string
	unitID      string
	pointID     string
	sourceID    string
	lineStart   int
	lineEnd     int
	score       float64
	sourcePaths []string // "outline", "fts"
	origin      string   // "" (rerank，默认) / OriginKPNExpansion，见 buildEvidence
}
