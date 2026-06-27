package session

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

type Parser struct {
	llm llm.LLMClient
}

func NewParser(client llm.LLMClient) *Parser {
	return &Parser{llm: client}
}

func (p *Parser) Parse(ctx context.Context, input string, state *SessionState) ParseResult {
	vars := map[string]string{
		"recent_subjects": formatRecentSubjects(state.Dialogue.RecentSubjects),
		"current_subject": truncate(state.Working.CurrentSubject, 60, "（空）"),
		"user_input":      truncate(input, 200, ""),
	}

	raw, err := p.llm.Complete(ctx, "session_parse.md", vars, "parse")
	if err != nil {
		return ParseResult{}
	}

	result, ok := repairLayer1(raw)
	if ok {
		return result
	}

	return p.retryFields(ctx, input, state, result)
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

var jsonBlockRe = regexp.MustCompile(`\{[^{}]*\}`)

type parseOutput struct {
	Intent  string `json:"intent"`
	Subject string `json:"subject"`
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

	return partial
}
