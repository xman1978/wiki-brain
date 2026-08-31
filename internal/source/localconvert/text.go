package localconvert

import (
	"bytes"
	"fmt"
	"os"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// ConvertTextToMarkdown handles .md/.markdown/.txt sources: normalize to
// UTF-8 and use as-is. Markdown already is the target format (mirrors the
// real FileView service's copyPassthrough for .md — see
// MarkdownConverter.java); plain text has no markup to lose, so it is valid
// Markdown unchanged too. This intentionally does not port FileView's
// txt-via-HTML-roundtrip-then-heading-post-process pipeline
// (ConvertWorker.java TARGET_MARKDOWN branch) — local mode does not aim to
// replicate FileView's heuristics, only to stop failing outright (see
// docs/impl/v1/local-file-convert.md 背景与定位).
func ConvertTextToMarkdown(srcPath string) ([]byte, error) {
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		return nil, fmt.Errorf("read text file: %w", err)
	}
	return normalizeToUTF8(raw), nil
}

// normalizeToUTF8 strips a UTF-8 BOM and, for content that isn't valid
// UTF-8, falls back to decoding as GBK — the common encoding for .txt files
// saved by Chinese-locale editors (Windows "ANSI"). Decode failure keeps the
// original bytes rather than failing the whole import over an encoding edge
// case.
func normalizeToUTF8(raw []byte) []byte {
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	if utf8.Valid(raw) {
		return raw
	}
	decoded, _, err := transform.Bytes(simplifiedchinese.GBK.NewDecoder(), raw)
	if err != nil {
		return raw
	}
	return decoded
}
