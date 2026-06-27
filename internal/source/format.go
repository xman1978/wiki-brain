package source

import (
	"path/filepath"
	"strings"
)

var supportedFormats = map[string]string{
	".doc": "word", ".docx": "word",
	".xls": "excel", ".xlsx": "excel",
	".ppt": "other", ".pptx": "other",
	".wps": "other", ".et": "other", ".dps": "other",
	".pdf": "pdf", ".ofd": "pdf",
	".rtf": "other", ".txt": "other",
	".md": "markdown", ".markdown": "markdown",
	".jpg": "image", ".jpeg": "image",
	".png": "image", ".bmp": "image",
	".tif": "image", ".tiff": "image",
}

var fileViewWhitelist = map[string]bool{
	".doc": true, ".docx": true, ".xls": true, ".xlsx": true,
	".ppt": true, ".pptx": true, ".wps": true, ".et": true, ".dps": true,
	".pdf": true, ".ofd": true, ".rtf": true, ".txt": true,
	".md": true, ".markdown": true,
	".jpg": true, ".jpeg": true, ".png": true, ".bmp": true,
	".tif": true, ".tiff": true,
}

func DetectFormat(fileName string) string {
	ext := strings.ToLower(filepath.Ext(fileName))
	if f, ok := supportedFormats[ext]; ok {
		return f
	}
	return "other"
}

func IsMarkdown(fileName string) bool {
	ext := strings.ToLower(filepath.Ext(fileName))
	return ext == ".md" || ext == ".markdown"
}

func IsSupportedFormat(fileName string) bool {
	ext := strings.ToLower(filepath.Ext(fileName))
	return fileViewWhitelist[ext]
}

func IsImageFormat(format string) bool {
	return format == "image"
}

func IsPDFOrImage(format string) bool {
	return format == "pdf" || format == "image"
}
