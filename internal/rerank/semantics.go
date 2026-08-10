package rerank

const ExtractPromptVersion = "v14"

type Semantics struct {
	UnitID        string
	SourceTheme   string
	ContentTheme  string
	Intent        string
	Object        string
	Scope         string
	PromptVersion string
}
