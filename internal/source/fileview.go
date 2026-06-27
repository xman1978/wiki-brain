package source

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type FileViewClient interface {
	ConvertToMarkdown(ctx context.Context, srcPath string) (markdown []byte, err error)
	ConvertToHTML(ctx context.Context, srcPath string) (html []byte, err error)
}

type fileViewClient struct {
	baseURL        string
	pollInterval   time.Duration
	maxPollSeconds int
	httpClient     *http.Client
}

func NewFileViewClient(baseURL string, pollIntervalMs, maxPollSeconds int) FileViewClient {
	return &fileViewClient{
		baseURL:        baseURL,
		pollInterval:   time.Duration(pollIntervalMs) * time.Millisecond,
		maxPollSeconds: maxPollSeconds,
		httpClient:     &http.Client{Timeout: 30 * time.Second},
	}
}

type convertResponse struct {
	Code int `json:"code"`
	Data struct {
		TaskID string `json:"taskId"`
		Status string `json:"status"`
	} `json:"data"`
}

// taskStatusResponse 匹配实际 FileView API：字段在顶层，不嵌套在 data 中。
type taskStatusResponse struct {
	TaskID      string `json:"taskId"`
	Status      string `json:"status"`
	MarkdownURL string `json:"markdownUrl"`
	HTMLURL     string `json:"htmlUrl"`
	Error       string `json:"error"`
}

func (c *fileViewClient) ConvertToMarkdown(ctx context.Context, srcPath string) ([]byte, error) {
	return c.convert(ctx, srcPath, "/api/convert/markdown", "markdownUrl")
}

func (c *fileViewClient) ConvertToHTML(ctx context.Context, srcPath string) ([]byte, error) {
	return c.convert(ctx, srcPath, "/api/convert/html", "htmlUrl")
}

func (c *fileViewClient) convert(ctx context.Context, srcPath, endpoint, urlField string) ([]byte, error) {
	taskID, status, err := c.upload(ctx, srcPath, endpoint)
	if err != nil {
		return nil, err
	}

	// 小文件可能在 convert 时直接完成，仍需查 task 获取结果 URL
	if status == "finished" || status == "processing" {
		resultURL, err := c.poll(ctx, taskID, urlField)
		if err != nil {
			return nil, err
		}
		return c.download(ctx, resultURL)
	}

	return nil, fmt.Errorf("fileview: unexpected status %q from convert", status)
}

func (c *fileViewClient) upload(ctx context.Context, srcPath, endpoint string) (taskID, status string, err error) {
	file, err := os.Open(srcPath)
	if err != nil {
		return "", "", fmt.Errorf("fileview: open file %s: %w", srcPath, err)
	}
	defer file.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", filepath.Base(srcPath))
	if err != nil {
		return "", "", fmt.Errorf("fileview: create form file: %w", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		return "", "", fmt.Errorf("fileview: copy file: %w", err)
	}

	if err := writer.WriteField("markdownToc", "false"); err != nil {
		return "", "", fmt.Errorf("fileview: write field: %w", err)
	}

	if err := writer.Close(); err != nil {
		return "", "", fmt.Errorf("fileview: close writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+endpoint, &body)
	if err != nil {
		return "", "", fmt.Errorf("fileview: create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fileview: upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusServiceUnavailable {
		return "", "", fmt.Errorf("fileview: service unavailable (queue full)")
	}
	if resp.StatusCode == http.StatusBadRequest {
		return "", "", fmt.Errorf("fileview: unsupported format")
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("fileview: upload returned status %d", resp.StatusCode)
	}

	var cr convertResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return "", "", fmt.Errorf("fileview: decode response: %w", err)
	}
	if cr.Code != 0 {
		return "", "", fmt.Errorf("fileview: upload error code %d", cr.Code)
	}

	return cr.Data.TaskID, cr.Data.Status, nil
}

func (c *fileViewClient) poll(ctx context.Context, taskID, urlField string) (string, error) {
	deadline := time.Now().Add(time.Duration(c.maxPollSeconds) * time.Second)

	// 首次立即查询，不等 ticker
	for {
		req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/task/"+taskID, nil)
		if err != nil {
			return "", fmt.Errorf("fileview: create poll request: %w", err)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("fileview: poll: %w", err)
		}

		var sr taskStatusResponse
		if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
			resp.Body.Close()
			return "", fmt.Errorf("fileview: decode poll response: %w", err)
		}
		resp.Body.Close()

		switch sr.Status {
		case "finished":
			var resultURL string
			if urlField == "markdownUrl" {
				resultURL = sr.MarkdownURL
			} else {
				resultURL = sr.HTMLURL
			}
			if resultURL == "" {
				return "", fmt.Errorf("fileview: finished but %s is empty", urlField)
			}
			return resultURL, nil
		case "failed":
			return "", fmt.Errorf("fileview: task failed: %s", sr.Error)
		case "processing":
			// wait and retry
		}

		if time.Now().After(deadline) {
			return "", fmt.Errorf("fileview: polling timeout after %d seconds", c.maxPollSeconds)
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(c.pollInterval):
		}
	}
}

func (c *fileViewClient) download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("fileview: create download request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fileview: download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fileview: download returned status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("fileview: read download body: %w", err)
	}

	return data, nil
}
