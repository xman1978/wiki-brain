package rerank

const ExtractPromptVersion = "v12"

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
