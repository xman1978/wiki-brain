package web

import "embed"

//go:embed index.html marked.min.js help.html manual.md vendor/file-viewer
var FS embed.FS
