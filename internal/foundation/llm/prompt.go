package llm

import (
	"fmt"
	"os"
	"strings"
)

type Prompt struct {
	Version string
	System  string
	User    string
	Schema  string
}

func LoadPrompt(path string, vars map[string]string) (*Prompt, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("llm: read prompt %s: %w", path, err)
	}
	return ParsePrompt(string(data), vars)
}

func ParsePrompt(content string, vars map[string]string) (*Prompt, error) {
	p := &Prompt{}

	body := content
	if strings.HasPrefix(strings.TrimSpace(content), "---") {
		parts := strings.SplitN(content, "---", 3)
		if len(parts) >= 3 {
			fm := parts[1]
			body = parts[2]
			for _, line := range strings.Split(fm, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "version:") {
					p.Version = strings.TrimSpace(strings.TrimPrefix(line, "version:"))
				}
			}
		}
	}

	sections := splitSections(body)

	p.System = sections["System"]
	p.User = sections["User"]
	p.Schema = extractJSONFromCodeBlock(sections["Schema"])

	for k, v := range vars {
		placeholder := "{{" + k + "}}"
		p.User = strings.ReplaceAll(p.User, placeholder, v)
	}

	return p, nil
}

func splitSections(body string) map[string]string {
	sections := make(map[string]string)
	var currentSection string
	var currentContent strings.Builder

	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			if currentSection != "" {
				sections[currentSection] = strings.TrimSpace(currentContent.String())
			}
			currentSection = strings.TrimPrefix(trimmed, "## ")
			currentContent.Reset()
			continue
		}
		if currentSection != "" {
			currentContent.WriteString(line)
			currentContent.WriteString("\n")
		}
	}
	if currentSection != "" {
		sections[currentSection] = strings.TrimSpace(currentContent.String())
	}

	return sections
}

func extractJSONFromCodeBlock(s string) string {
	if s == "" {
		return ""
	}
	start := strings.Index(s, "```json")
	if start == -1 {
		start = strings.Index(s, "```")
		if start == -1 {
			return strings.TrimSpace(s)
		}
	}
	after := s[start:]
	firstNewline := strings.Index(after, "\n")
	if firstNewline == -1 {
		return ""
	}
	rest := after[firstNewline+1:]
	end := strings.Index(rest, "```")
	if end == -1 {
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(rest[:end])
}
