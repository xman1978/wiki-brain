package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	gosdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jxman78/wiki-brain/internal/retrieval"
)

// ── import_file ──────────────────────────────────────────────────────────

type ImportFileInput struct {
	FilePath      string `json:"file_path,omitempty" jsonschema:"本地绝对文件路径，与 content_base64 二选一，文件已在本地磁盘时使用"`
	ContentBase64 string `json:"content_base64,omitempty" jsonschema:"文件内容的 base64 编码，与 file_path 二选一，材料只存在于 Agent 侧、尚未落盘时使用"`
	Filename      string `json:"filename,omitempty" jsonschema:"content_base64 模式下必填，用于推断文件格式与标题"`
	Origin        string `json:"origin,omitempty" jsonschema:"来源标记，默认 agent_generated"`
	OriginPageID  string `json:"origin_page_id,omitempty"`
}

type ImportFileOutput struct {
	SourceID string `json:"source_id"`
	Status   string `json:"status"`
	Title    string `json:"title"`
	Format   string `json:"format"`
}

func (s *Server) importFile(ctx context.Context, _ *gosdk.CallToolRequest, in ImportFileInput) (*gosdk.CallToolResult, ImportFileOutput, error) {
	hasPath := in.FilePath != ""
	hasContent := in.ContentBase64 != ""

	switch {
	case hasPath && hasContent:
		return nil, ImportFileOutput{}, fmt.Errorf("file_path and content_base64 are mutually exclusive")
	case !hasPath && !hasContent:
		return nil, ImportFileOutput{}, fmt.Errorf("file_path or content_base64 is required")
	}

	var (
		reader   *bytes.Reader
		filename string
	)

	if hasPath {
		data, err := os.ReadFile(in.FilePath)
		if err != nil {
			return nil, ImportFileOutput{}, fmt.Errorf("read file_path: %w", err)
		}
		reader = bytes.NewReader(data)
		filename = filepath.Base(in.FilePath)
	} else {
		if strings.TrimSpace(in.Filename) == "" {
			return nil, ImportFileOutput{}, fmt.Errorf("filename is required when content_base64 is used")
		}
		data, err := base64.StdEncoding.DecodeString(in.ContentBase64)
		if err != nil {
			return nil, ImportFileOutput{}, fmt.Errorf("decode content_base64: %w", err)
		}
		reader = bytes.NewReader(data)
		filename = in.Filename
	}

	origin := in.Origin
	if origin == "" {
		origin = "agent_generated"
	}

	src, err := s.sourceSvc.ImportWithOrigin(ctx, filename, reader, origin, in.OriginPageID)
	if err != nil {
		return nil, ImportFileOutput{}, err
	}

	// Bounded synchronous wait (docs/design/mcp.md「不做导入状态轮询工具」):
	// most personal documents finish extraction well within this window; a
	// slower one just comes back as status=processing, still with a usable
	// source_id, no separate polling tool needed.
	deadline := time.Now().Add(s.cfg.ImportWaitTimeout)
	for time.Now().Before(deadline) {
		if src.Status == "completed" || src.Status == "failed" {
			break
		}
		time.Sleep(s.cfg.ImportPollInterval)
		cur, err := s.sourceStore.GetByID(src.SourceID)
		if err != nil {
			break
		}
		src = cur
	}

	return nil, ImportFileOutput{
		SourceID: src.SourceID,
		Status:   src.Status,
		Title:    src.Title,
		Format:   src.Format,
	}, nil
}

// ── retrieve ─────────────────────────────────────────────────────────────

type RetrieveInput struct {
	Question        string `json:"question" jsonschema:"要检索的问题"`
	ForceFull       bool   `json:"force_full,omitempty" jsonschema:"跳过快路径，强制走完整检索流程，默认 false"`
	DocCategoryHint string `json:"doc_category_hint,omitempty" jsonschema:"期望的材料体裁（自由文本，如“故障案例”“制度原文”），可选"`
}

type EvidenceItem struct {
	Content  string   `json:"content"`
	Citation Citation `json:"citation"`
	Role     string   `json:"role"`
}

type ConflictItem struct {
	Content  string   `json:"content"`
	Citation Citation `json:"citation"`
	Note     string   `json:"note"`
}

type RetrieveOutput struct {
	Question           string         `json:"question"`
	DirectEvidence     []EvidenceItem `json:"direct_evidence"`
	SupportingEvidence []EvidenceItem `json:"supporting_evidence"`
	Conflicts          []ConflictItem `json:"conflicts,omitempty"`
	GapReason          string         `json:"gap_reason,omitempty"`
}

func (s *Server) retrieve(ctx context.Context, _ *gosdk.CallToolRequest, in RetrieveInput) (*gosdk.CallToolResult, RetrieveOutput, error) {
	if strings.TrimSpace(in.Question) == "" {
		return nil, RetrieveOutput{}, fmt.Errorf("question is required")
	}

	es, err := s.retrievalSvc.RetrieveWithProgress(ctx, retrieval.QueryContext{
		Question:        in.Question,
		ForceFull:       in.ForceFull,
		DocCategoryHint: in.DocCategoryHint,
	}, nil)
	if err != nil {
		return nil, RetrieveOutput{}, err
	}

	resolver := newCitationResolver(s.sourceStore)
	out := RetrieveOutput{Question: es.Question, GapReason: es.GapReason}

	// Wiki 直答路径没有 DirectEvidence/Supporting（只有 CitedPointIDs），见
	// internal/retrieval/types.go EvidenceSet 注释。回落到按 point_id 查 KP
	// 内容+来源，复用同一套引用解析，保证 wiki 命中不会在 MCP 层被丢弃为空。
	if es.PathType == retrieval.PathTypeWiki {
		items, err := s.wikiCitationsFromPoints(resolver, es.CitedPointIDs)
		if err != nil {
			return nil, RetrieveOutput{}, err
		}
		out.DirectEvidence = items
		return nil, out, nil
	}

	out.DirectEvidence = mapEvidence(resolver, es.DirectEvidence, "direct")
	out.SupportingEvidence = mapEvidence(resolver, es.Supporting, "supporting")
	out.Conflicts = mapConflicts(resolver, es.Conflicts)

	return nil, out, nil
}

func mapEvidence(resolver *citationResolver, list []retrieval.Evidence, role string) []EvidenceItem {
	out := make([]EvidenceItem, 0, len(list))
	for _, e := range list {
		ref, _ := extractSourceRef(e.SourceRef)
		out = append(out, EvidenceItem{
			Content:  e.Content,
			Citation: resolver.resolve(ref.SourceID, ref.LineStart),
			Role:     role,
		})
	}
	return out
}

func mapConflicts(resolver *citationResolver, list []retrieval.ConflictEvidence) []ConflictItem {
	out := make([]ConflictItem, 0, len(list))
	for _, c := range list {
		ref, _ := extractSourceRef(c.SourceRef)
		out = append(out, ConflictItem{
			Content:  c.Content,
			Citation: resolver.resolve(ref.SourceID, ref.LineStart),
			Note:     "与其他证据存在冲突，需自行判断取舍",
		})
	}
	return out
}
