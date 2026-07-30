# LLM Provider Response Format Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `response_format.type` (`json_object` | `json_schema`) configurable per LLM provider, defaulting to `json_object`, and expose it on the provider settings form.

**Architecture:** Persist `response_format` on `llm_providers`; load it into `ProviderRuntime`; when marshalling JSON-mode chat requests, emit `{"type": <value>}` instead of a hardcoded `json_object`. Non-JSON Complete/stream paths omit the field.

**Tech Stack:** Go, SQLite migrations, `internal/foundation/llm`, `internal/llmconfig`, single-file `web/index.html`

**Spec:** `docs/superpowers/specs/2026-07-30-llm-response-format-design.md`

## Global Constraints

- Request body is only `{"type":"<value>"}` — never attach OpenAI Structured Outputs `json_schema` object
- Allowed values: exactly `json_object` and `json_schema`; empty/missing → normalize to `json_object`; other values → `ErrInvalidInput`
- Only apply when `jsonObject == true` (`CompleteJSON`); Complete / stream never set `response_format`
- Default for new/imported providers: `json_object` (preserve current behavior)
- Do not add `text` option; do not per-purpose config; do not auto-infer from `platform`

## File map

| File | Responsibility |
|------|----------------|
| `internal/foundation/db/migrations/038_llm_response_format.sql` | Add column with default |
| `internal/foundation/llm/models.go` | Constants + `ProviderRuntime.ResponseFormat` |
| `internal/foundation/llm/request_builder.go` | Use configured type in JSON mode |
| `internal/foundation/llm/request_builder_test.go` | Table-driven coverage |
| `internal/foundation/llm/client.go` | Pass `c.provider.ResponseFormat` into marshal |
| `internal/llmconfig/store.go` | Column CRUD, validate, `ProviderToRuntime` |
| `internal/llmconfig/store_validate_test.go` | ValidateProvider tests (new) |
| `internal/llmconfig/service.go` | Bootstrap / SnapshotFromBootstrap default |
| `web/index.html` | Form select + collect/save |

---

### Task 1: Request builder uses configurable type

**Files:**
- Modify: `internal/foundation/llm/models.go`
- Modify: `internal/foundation/llm/request_builder.go`
- Modify: `internal/foundation/llm/request_builder_test.go`
- Modify: `internal/foundation/llm/client.go` (call sites only — after signature change)

**Interfaces:**
- Produces: constants `ResponseFormatJSONObject = "json_object"`, `ResponseFormatJSONSchema = "json_schema"`
- Produces: `ProviderRuntime.ResponseFormat string`
- Produces: `func marshalChatRequest(platform Platform, mc ModelParams, messages []chatMessage, jsonObject, stream bool, responseFormat string) ([]byte, error)`

- [ ] **Step 1: Write failing tests** in `request_builder_test.go`

```go
func TestMarshalChatRequest_ResponseFormat(t *testing.T) {
	mc := ModelParams{Model: "m", Temperature: 0}
	msgs := []chatMessage{{Role: "user", Content: "hi"}}
	cases := []struct {
		name           string
		jsonObject     bool
		responseFormat string
		wantType       string // empty => key must be absent
	}{
		{"json_mode_object", true, "json_object", "json_object"},
		{"json_mode_schema", true, "json_schema", "json_schema"},
		{"json_mode_empty_defaults", true, "", "json_object"},
		{"non_json_omits", false, "json_schema", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := marshalChatRequest(PlatformOpenAICompatible, mc, msgs, tc.jsonObject, false, tc.responseFormat)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(body, &m); err != nil {
				t.Fatal(err)
			}
			rf, ok := m["response_format"]
			if tc.wantType == "" {
				if ok {
					t.Fatalf("response_format should be omitted, got %v", rf)
				}
				return
			}
			if !ok {
				t.Fatal("expected response_format")
			}
			obj, ok := rf.(map[string]any)
			if !ok {
				t.Fatalf("response_format type %T", rf)
			}
			if obj["type"] != tc.wantType {
				t.Fatalf("type = %v, want %s", obj["type"], tc.wantType)
			}
			if len(obj) != 1 {
				t.Fatalf("response_format must only contain type, got %v", obj)
			}
		})
	}
}
```

Also update existing thinking tests to pass `""` as the new last argument (or they will not compile).

- [ ] **Step 2: Run tests — expect FAIL** (compile error or wrong hardcoded type)

Run: `go test ./internal/foundation/llm/ -run 'TestMarshalChatRequest' -count=1`

- [ ] **Step 3: Add constants + field on `ProviderRuntime`**

In `models.go`:

```go
const (
	ResponseFormatJSONObject = "json_object"
	ResponseFormatJSONSchema = "json_schema"
)

// ProviderRuntime ...
type ProviderRuntime struct {
	ProviderID      string
	Platform        Platform
	BaseURL         string
	APIKey          string
	TimeoutSeconds  int
	MaxRetries      int
	ResponseFormat  string
	Models          map[string]ModelParams
}
```

- [ ] **Step 4: Implement marshal change**

```go
func marshalChatRequest(platform Platform, mc ModelParams, messages []chatMessage, jsonObject, stream bool, responseFormat string) ([]byte, error) {
	// ... existing fields ...
	if jsonObject {
		rf := responseFormat
		if rf == "" {
			rf = ResponseFormatJSONObject
		}
		reqBody["response_format"] = map[string]string{"type": rf}
	}
	// ...
}
```

- [ ] **Step 5: Update `client.go` call sites**

```go
// call:
bodyBytes, err := marshalChatRequest(c.provider.Platform, mc, messages, jsonObject, false, c.provider.ResponseFormat)

// CompleteStreamWithParams:
bodyBytes, err := marshalChatRequest(c.provider.Platform, mc, messages, false, true, c.provider.ResponseFormat)
```

- [ ] **Step 6: Run tests — expect PASS**

Run: `go test ./internal/foundation/llm/ -run 'TestMarshalChatRequest' -count=1`

- [ ] **Step 7: Commit** (only if user asked to commit in this session)

```bash
git add internal/foundation/llm/models.go internal/foundation/llm/request_builder.go internal/foundation/llm/request_builder_test.go internal/foundation/llm/client.go
git commit -m "$(cat <<'EOF'
feat(llm): configurable response_format.type in chat requests

EOF
)"
```

---

### Task 2: DB column + store validation + runtime wiring

**Files:**
- Create: `internal/foundation/db/migrations/038_llm_response_format.sql`
- Modify: `internal/llmconfig/store.go`
- Create: `internal/llmconfig/store_validate_test.go`
- Modify: `internal/llmconfig/service.go` (`SnapshotFromBootstrap` / bootstrap `Provider` leave field empty so Validate fills default)

**Interfaces:**
- Consumes: `llm.ResponseFormatJSONObject`, `llm.ResponseFormatJSONSchema`
- Produces: `Provider.ResponseFormat` persisted and returned on GET; `ProviderToRuntime` copies field

- [ ] **Step 1: Write failing ValidateProvider tests**

```go
package llmconfig

import (
	"errors"
	"testing"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

func validProvider() Provider {
	return Provider{
		Name:           "t",
		Platform:       llm.PlatformOpenAICompatible,
		BaseURL:        "http://localhost/v1",
		TimeoutSeconds: 120,
	}
}

func TestValidateProvider_ResponseFormat(t *testing.T) {
	p := validProvider()
	p.ResponseFormat = ""
	if err := ValidateProvider(&p); err != nil {
		t.Fatal(err)
	}
	if p.ResponseFormat != llm.ResponseFormatJSONObject {
		t.Fatalf("got %q", p.ResponseFormat)
	}

	p = validProvider()
	p.ResponseFormat = llm.ResponseFormatJSONSchema
	if err := ValidateProvider(&p); err != nil {
		t.Fatal(err)
	}

	p = validProvider()
	p.ResponseFormat = "text"
	if err := ValidateProvider(&p); err == nil || !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
}
```

- [ ] **Step 2: Run — expect FAIL** (field / validation missing)

Run: `go test ./internal/llmconfig/ -run TestValidateProvider_ResponseFormat -count=1`

- [ ] **Step 3: Add migration**

`038_llm_response_format.sql`:

```sql
ALTER TABLE llm_providers ADD COLUMN response_format TEXT NOT NULL DEFAULT 'json_object';
```

- [ ] **Step 4: Extend `Provider` + SQL**

- Add `ResponseFormat string \`json:"response_format"\`` to `Provider`
- Update `ListProviders` / `GetProvider` SELECT to include `response_format`
- Update `InsertProvider` / `UpdateProvider` to write the column
- Update `scanProvider` / `scanProviderRow` to Scan into `p.ResponseFormat`
- In `ValidateProvider`:

```go
switch strings.TrimSpace(p.ResponseFormat) {
case "", llm.ResponseFormatJSONObject:
	p.ResponseFormat = llm.ResponseFormatJSONObject
case llm.ResponseFormatJSONSchema:
	p.ResponseFormat = llm.ResponseFormatJSONSchema
default:
	return fmt.Errorf("%w: invalid response_format", ErrInvalidInput)
}
```

- In `ProviderToRuntime`:

```go
return &llm.ProviderRuntime{
	// ...existing...
	ResponseFormat: p.ResponseFormat,
	Models:         map[string]llm.ModelParams{},
}
```

- [ ] **Step 5: Run Validate test — PASS**

Run: `go test ./internal/llmconfig/ -run TestValidateProvider_ResponseFormat -count=1`

- [ ] **Step 6: Commit** (if requested)

```bash
git add internal/foundation/db/migrations/038_llm_response_format.sql internal/llmconfig/store.go internal/llmconfig/store_validate_test.go
git commit -m "$(cat <<'EOF'
feat(llmconfig): persist provider response_format

EOF
)"
```

---

### Task 3: Web UI — 响应格式 select

**Files:**
- Modify: `web/index.html` (`renderLlmProviderForm`, `collectProviderFromForm`, new-provider defaults)

**Interfaces:**
- Consumes: API field `response_format`
- Produces: POST/PUT body includes `response_format`

- [ ] **Step 1: Add form field** after 平台 in `renderLlmProviderForm`

```javascript
var rf = pr.response_format || 'json_object';
html += fieldRow('响应格式', '<select id="llm_rf_' + id + '" class="llm-field">' +
  '<option value="json_object"' + (rf === 'json_object' ? ' selected' : '') + '>json_object</option>' +
  '<option value="json_schema"' + (rf === 'json_schema' ? ' selected' : '') + '>json_schema</option>' +
  '</select>');
```

- [ ] **Step 2: Default new provider object** (where `__new__` form is seeded) include `response_format: 'json_object'` if not already.

- [ ] **Step 3: Collect on save**

In `collectProviderFromForm`:

```javascript
response_format: document.getElementById('llm_rf_' + id).value
```

- [ ] **Step 4: Manual smoke** (or skip if no browser): open 模型设置 → edit provider → see 响应格式 → save → reload page → value retained. Switch to `json_schema` and confirm a JSON LLM call no longer sends `json_object`.

- [ ] **Step 5: Commit** (if requested)

```bash
git add web/index.html
git commit -m "$(cat <<'EOF'
feat(web): provider response_format selector

EOF
)"
```

---

### Task 4: Full package regression

- [ ] **Step 1: Run**

```bash
go test ./internal/foundation/llm/ ./internal/llmconfig/ -count=1
```

Expected: PASS

- [ ] **Step 2: Optional wider check** if time permits

```bash
go test ./internal/foundation/... ./internal/llmconfig/... -count=1
```

---

## Spec coverage checklist

| Spec requirement | Task |
|------------------|------|
| Provider-level `response_format` | 2 |
| Values `json_object` \| `json_schema`, default object | 1–2 |
| Body only `{"type":...}` | 1 |
| JSON mode only | 1 |
| Migration DEFAULT | 2 |
| Validate empty → default / invalid → error | 2 |
| Web 响应格式 select | 3 |
| Bootstrap / existing rows default | 2 (column DEFAULT + Validate) |
| No Structured Outputs schema object | 1 (assert `len(obj)==1`) |

## Self-review notes

- No `text` option (per non-goals).
- `DiscoverModels` does not need `ResponseFormat` (no chat JSON call).
- Existing thinking tests must be updated for the new `marshalChatRequest` arity in Task 1.
