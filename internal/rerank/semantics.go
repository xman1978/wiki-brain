package rerank

// ExtractPromptVersion stamps knowledge_points.semantics_prompt_version.
// Bump whenever the points schema in unit_extract.md / unit_extract_retry.md /
// unit_gap_extract.md (new imports) or kp_semantics_extract.md (backfill)
// changes what it asks the model to produce for these fields.
const ExtractPromptVersion = "v2"

// Semantics is a single knowledge point's rerank semantics — one KP can have
// a different object/scope than its sibling KPs in the same knowledge unit
// (docs/design/wiki-single-tier-revision.md's KU/KP distinction applies here
// too: a KU is often a coarse container, its KPs are not interchangeable).
type Semantics struct {
	PointID       string
	SourceTheme   string
	ContentTheme  string
	Object        string
	Scope         string
	PromptVersion string
}
