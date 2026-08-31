// Package mcp exposes two of wiki-brain's existing capabilities — file
// import and knowledge retrieval — to external AI Agent platforms over the
// Model Context Protocol. It is a thin wrapper: all extraction, retrieval
// and learning logic lives unchanged in internal/source, internal/unit and
// internal/retrieval. See docs/design/mcp.md and docs/impl/v1/mcp.md.
package mcp

import (
	"net/http"
	"time"

	gosdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jxman78/wiki-brain/internal/retrieval"
	"github.com/jxman78/wiki-brain/internal/source"
	"github.com/jxman78/wiki-brain/internal/unit"
)

const (
	implName    = "wiki-brain"
	implVersion = "1.0.0"

	defaultImportWaitTimeout  = 20 * time.Second
	defaultImportPollInterval = 500 * time.Millisecond
)

// Config mirrors config.McpConfig; kept as a plain struct here so this
// package doesn't import internal/foundation/config.
type Config struct {
	ImportWaitTimeout  time.Duration
	ImportPollInterval time.Duration
}

func (c Config) withDefaults() Config {
	if c.ImportWaitTimeout <= 0 {
		c.ImportWaitTimeout = defaultImportWaitTimeout
	}
	if c.ImportPollInterval <= 0 {
		c.ImportPollInterval = defaultImportPollInterval
	}
	return c
}

// Server holds the dependencies the two MCP tools call into. Everything here
// is already constructed and wired by cmd/server/main.go for the existing
// HTTP API — Server does not own a database connection or queue of its own.
type Server struct {
	sourceSvc    *source.Service
	sourceStore  *source.Store
	unitStore    *unit.Store
	retrievalSvc *retrieval.Service
	cfg          Config
}

func NewServer(sourceSvc *source.Service, sourceStore *source.Store, unitStore *unit.Store, retrievalSvc *retrieval.Service, cfg Config) *Server {
	return &Server{
		sourceSvc:    sourceSvc,
		sourceStore:  sourceStore,
		unitStore:    unitStore,
		retrievalSvc: retrievalSvc,
		cfg:          cfg.withDefaults(),
	}
}

// Handler returns an http.Handler serving MCP over Streamable HTTP
// (docs/impl/v1/mcp.md「运行方式」) — mounted on the same long-running HTTP
// server as the existing REST API, on one shared database connection and
// service graph, rather than as a separate stdio subprocess that would need
// its own copy of the entire service wiring in cmd/server/main.go.
func (s *Server) Handler() http.Handler {
	srv := gosdk.NewServer(&gosdk.Implementation{Name: implName, Version: implVersion}, nil)

	gosdk.AddTool(srv, &gosdk.Tool{
		Name: "import_file",
		Description: "导入一个文件到知识大脑，走完整的知识抽取流程后即可被 retrieve 检索到。" +
			"file_path（本地已有文件）与 content_base64（内容持有但未落盘）二选一提供。",
	}, s.importFile)

	gosdk.AddTool(srv, &gosdk.Tool{
		Name:        "retrieve",
		Description: "针对一个问题检索知识大脑中的证据，返回证据原文与可定位到本地文件具体章节的引用，不做答案合成。",
	}, s.retrieve)

	return gosdk.NewStreamableHTTPHandler(func(*http.Request) *gosdk.Server { return srv }, nil)
}
