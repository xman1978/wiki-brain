package sysconfig

import (
	"context"
	"sync/atomic"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/source"
	"github.com/jxman78/wiki-brain/internal/source/localconvert"
)

// DynamicFileViewClient implements source.FileViewClient by picking the
// remote FileView service or the built-in local conversion fallback per
// call, based on the latest FileViewSettings — swapped in via Update with
// no restart required (系统设置 → 文件转换服务, docs/impl/v1/local-file-convert.md).
type DynamicFileViewClient struct {
	llmClient llm.LLMClient
	settings  atomic.Pointer[FileViewSettings]
}

var _ source.FileViewClient = (*DynamicFileViewClient)(nil)
var _ source.ModeReporter = (*DynamicFileViewClient)(nil)

func NewDynamicFileViewClient(llmClient llm.LLMClient, initial FileViewSettings) *DynamicFileViewClient {
	c := &DynamicFileViewClient{llmClient: llmClient}
	c.Update(initial)
	return c
}

func (c *DynamicFileViewClient) Update(s FileViewSettings) {
	c.settings.Store(&s)
}

func (c *DynamicFileViewClient) Mode() string {
	if c.settings.Load().UseRemote {
		return "remote"
	}
	return "local"
}

func (c *DynamicFileViewClient) delegate() source.FileViewClient {
	s := *c.settings.Load()
	if s.UseRemote {
		return source.NewFileViewClient(s.BaseURL, s.PollIntervalMs, s.TimeoutSeconds)
	}
	return localconvert.NewLocalConvertClient(c.llmClient, localconvert.OCRSettings{
		Enabled:  s.OCREnabled,
		MaxPages: s.OCRMaxPages,
	})
}

func (c *DynamicFileViewClient) ConvertToMarkdown(ctx context.Context, srcPath string) ([]byte, error) {
	return c.delegate().ConvertToMarkdown(ctx, srcPath)
}

func (c *DynamicFileViewClient) ConvertToHTML(ctx context.Context, srcPath string) ([]byte, error) {
	return c.delegate().ConvertToHTML(ctx, srcPath)
}
