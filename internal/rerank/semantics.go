package rerank

const ExtractPromptVersion = "v13"

type Semantics struct {
	UnitID        string
	SourceTheme   string
	ContentTheme  string
	Intent        string
	Object        string
	Scope         string
	PromptVersion string
}
