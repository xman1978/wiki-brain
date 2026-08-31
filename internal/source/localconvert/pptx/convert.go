package pptx

import "fmt"

// Convert extracts a .pptx from disk, renders it to Markdown, and runs the
// cleanup pass. It returns the Markdown; callers handle output (file/stdout).
func Convert(input string) (string, error) {
	deck, err := ExtractFile(input)
	if err != nil {
		return "", fmt.Errorf("extract: %w", err)
	}
	return PostprocessText(ToMarkdown(deck)), nil
}
