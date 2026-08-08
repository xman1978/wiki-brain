package session

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

// DomainEntry is one row from the domains catalog for session_parse domain routing.
type DomainEntry struct {
	ID          string
	Name        string
	Description string
}

// DomainCatalog supplies domain_list for the merged parse+domain prompt.
type DomainCatalog interface {
	ListDomainEntries() ([]DomainEntry, error)
}

type Parser struct {
	llm     llm.LLMClient
	domains DomainCatalog
}

func NewParser(client llm.LLMClient) *Parser {
	return &Parser{llm: client}
}

func (p *Parser) SetDomainCatalog(c DomainCatalog) {
	p.domains = c
}

func (p *Parser) Parse(ctx context.Context, input string, state *SessionState) ParseResult {
	vars := map[string]string{
		"last_question": truncate(state.Dialogue.LastQuestion, 100, "（无）"),
		"last_answer":   truncateTail(state.Working.StepSummary, 300, "（无）"),
		"last_parse":    formatLastParse(state),
		"user_input":    truncate(input, 200, ""),
		"domain_list":   p.formatDomainList(),
	}

	raw, err := p.llm.Complete(ctx, "session_parse.md", vars, "classification")
	if err != nil {
		return ParseResult{}
	}

	result, ok := repairLayer1(raw)
	if ok {
		return result
	}

	return p.retryFields(ctx, input, state, result)
}

func (p *Parser) formatDomainList() string {
	if p.domains == nil {
		return "（无）"
	}
	entries, err := p.domains.ListDomainEntries()
	if err != nil || len(entries) == 0 {
		return "（无）"
	}
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "[%s] %s：%s\n", e.ID, e.Name, e.Description)
	}
	return b.String()
}

func formatRecentSubjects(subjects []string) string {
	if len(subjects) == 0 {
		return "[]"
	}
	var items []string
	for i, s := range subjects {
		if i >= 3 {
			break
		}
		if len([]rune(s)) > 60 {
			s = string([]rune(s)[:60])
		}
		items = append(items, fmt.Sprintf("%q", s))
	}
	return "[" + strings.Join(items, ",") + "]"
}

func truncate(s string, maxRunes int, fallback string) string {
	if s == "" {
		if fallback != "" {
			return fallback
		}
		return ""
	}
	runes := []rune(s)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return s
}

// truncateTail keeps the last maxRunes runes: answers state their
// conclusions at the end, so the tail is the information-dense part.
func truncateTail(s string, maxRunes int, fallback string) string {
	if s == "" {
		return fallback
	}
	runes := []rune(s)
	if len(runes) > maxRunes {
		return "……" + string(runes[len(runes)-maxRunes:])
	}
	return s
}

func formatLastParse(state *SessionState) string {
	d := state.Dialogue
	if d.Subject == "" && d.Audience == "" && d.Intent == "" && d.Constraint == "" {
		return "（无）"
	}
	return fmt.Sprintf(`{"subject":%q,"audience":%q,"intent":%q,"constraint":%q}`,
		d.Subject, d.Audience, d.Intent, d.Constraint)
}

// Allow nested arrays in domain_ids; still a single JSON object.
var jsonBlockRe = regexp.MustCompile(`\{(?:[^{}]|\[(?:[^\[\]])*\])*\}`)

type parseOutput struct {
	DomainIDs          []string `json:"domain_ids"`
	Intent             string   `json:"intent"`
	Subject            string   `json:"subject"`
	Audience           string   `json:"audience"`
	Constraint         string   `json:"constraint"`
	StandaloneQuestion string   `json:"standalone_question"`
}

func repairLayer1(raw string) (ParseResult, bool) {
	result := ParseResult{}

	match := jsonBlockRe.FindString(raw)
	if match == "" {
		return result, false
	}

	var out parseOutput
	if err := json.Unmarshal([]byte(match), &out); err != nil {
		return result, false
	}

	runes := []rune(out.Intent)
	if len(runes) > 50 {
		out.Intent = string(runes[:50])
	}
	result.Intent = out.Intent

	subjectRunes := []rune(out.Subject)
	if len(subjectRunes) > 100 {
		out.Subject = string(subjectRunes[:100])
	}
	result.Subject = out.Subject

	audienceRunes := []rune(out.Audience)
	if len(audienceRunes) > 50 {
		out.Audience = string(audienceRunes[:50])
	}
	result.Audience = out.Audience

	result.Constraint = out.Constraint
	if out.DomainIDs == nil {
		result.DomainIDs = []string{}
	} else {
		result.DomainIDs = out.DomainIDs
	}

	sqRunes := []rune(out.StandaloneQuestion)
	if len(sqRunes) > 200 {
		out.StandaloneQuestion = string(sqRunes[:200])
	}
	result.StandaloneQuestion = out.StandaloneQuestion

	valid := result.Intent != ""
	return result, valid
}

func (p *Parser) retryFields(ctx context.Context, input string, state *SessionState, partial ParseResult) ParseResult {
	if partial.Intent == "" {
		vars := map[string]string{"user_input": truncate(input, 200, "")}
		raw, err := p.llm.Complete(ctx, "session_retry_intent.md", vars, "parse")
		if err == nil && strings.TrimSpace(raw) != "" {
			r := []rune(strings.TrimSpace(raw))
			if len(r) > 50 {
				r = r[:50]
			}
			partial.Intent = string(r)
		}
	}
	if partial.DomainIDs == nil {
		partial.DomainIDs = []string{}
	}

	return partial
}
