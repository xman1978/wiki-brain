package mcp

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/jxman78/wiki-brain/internal/retrieval"
	"github.com/jxman78/wiki-brain/internal/source"
)

// Citation is the human-readable, locatable form of an internal
// {source_id, line_start, line_end} reference — see docs/design/mcp.md 第 3
// 节「为什么检索结果里的来源要解析成人类可读的引用」。
type Citation struct {
	SourceTitle string `json:"source_title"`
	Section     string `json:"section,omitempty"`
	Link        string `json:"link"`
	// DocCategoryName is the source's document-genre classification
	// (docs/design/doc-category.md), surfaced unconditionally — independent
	// of whether the retrieve call carried a doc_category_hint — so an Agent
	// can see each evidence item's genre even when it didn't filter by one
	// (docs/impl/v1/mcp.md 3b 节). Empty when the source is unclassified.
	DocCategoryName string `json:"doc_category_name,omitempty"`
}

// citationResolver resolves SourceRef -> Citation, caching each source's
// metadata and outline list for the lifetime of one retrieve call so that
// evidence items sharing the same source_id don't each re-query the store
// (see docs/impl/v1/mcp.md 步骤 3「引用解析」).
type citationResolver struct {
	store *source.Store

	mu            sync.Mutex
	sourceCache   map[string]*source.Source
	outlineCache  map[string][]source.Outline
	categoryCache map[string]string
}

func newCitationResolver(store *source.Store) *citationResolver {
	return &citationResolver{
		store:         store,
		sourceCache:   make(map[string]*source.Source),
		outlineCache:  make(map[string][]source.Outline),
		categoryCache: make(map[string]string),
	}
}

// resolve looks up sourceID's title and markdown path, then finds the
// narrowest outline node whose [line_start, line_end] contains lineStart to
// use as the section anchor. A source/outline lookup failure degrades to an
// empty Citation rather than failing the whole retrieve call — one bad
// reference shouldn't hide the rest of the evidence.
func (r *citationResolver) resolve(sourceID string, lineStart int) Citation {
	if sourceID == "" {
		return Citation{}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	src, ok := r.sourceCache[sourceID]
	if !ok {
		s, err := r.store.GetByID(sourceID)
		if err != nil {
			r.sourceCache[sourceID] = nil
			src = nil
		} else {
			src = s
			r.sourceCache[sourceID] = s
		}
	}
	if src == nil {
		return Citation{}
	}

	outlines, ok := r.outlineCache[sourceID]
	if !ok {
		list, err := r.store.GetOutlines(sourceID)
		if err != nil {
			list = nil
		}
		outlines = list
		r.outlineCache[sourceID] = outlines
	}

	section := ""
	bestWidth := -1
	for _, o := range outlines {
		if lineStart < o.LineStart || lineStart > o.LineEnd {
			continue
		}
		width := o.LineEnd - o.LineStart
		if bestWidth == -1 || width < bestWidth {
			bestWidth = width
			section = o.Title
		}
	}

	link := "file://" + src.MarkdownPath
	if section != "" {
		link += "#" + slugify(section)
	}

	categoryName := ""
	if src.DocCategoryID.Valid && src.DocCategoryID.String != "" {
		categoryID := src.DocCategoryID.String
		name, ok := r.categoryCache[categoryID]
		if !ok {
			resolved, err := r.store.GetDocCategoryName(categoryID)
			if err != nil {
				resolved = ""
			}
			name = resolved
			r.categoryCache[categoryID] = name
		}
		categoryName = name
	}

	return Citation{
		SourceTitle:     src.Title,
		Section:         section,
		Link:            link,
		DocCategoryName: categoryName,
	}
}

// extractSourceRef decodes Evidence/ConflictEvidence's SourceRef
// (json.RawMessage `{"source_id","line_start","line_end"}`, docs CLAUDE.md
// 关键约定). A malformed/empty payload just yields a zero SourceRef, which
// resolve() turns into an empty Citation rather than an error.
func extractSourceRef(raw json.RawMessage) (retrieval.SourceRef, error) {
	var ref retrieval.SourceRef
	if len(raw) == 0 {
		return ref, nil
	}
	err := json.Unmarshal(raw, &ref)
	return ref, err
}

// slugify follows the GitHub heading-anchor convention (lowercase, non
// alnum runs collapse to a single "-", trimmed) so links produced here open
// correctly in the local Markdown viewers/editors most AI Agent platforms
// invoke — see docs/design/mcp.md 第 3 节。CJK characters are kept as-is
// (GitHub's own anchor algorithm does not transliterate them either).
func slugify(title string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r >= 0x4e00 && r <= 0x9fff:
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteRune('-')
				lastDash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}
