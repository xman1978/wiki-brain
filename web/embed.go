package web

import "embed"

//go:embed index.html marked.min.js vendor/file-viewer
var FS embed.FS
