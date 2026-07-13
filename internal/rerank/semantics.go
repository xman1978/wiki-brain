package rerank

const ExtractPromptVersion = "v1"

type Semantics struct {
	UnitID        string
	SourceTheme   string
	ContentTheme  string
	Intent        string
	Object        string
	Scope         string
	KeyFacts      []string
	PromptVersion string
}
