package retrieval

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/search/query"
	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/activation"
	"github.com/jxman78/wiki-brain/internal/evidence"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/session"
	"github.com/jxman78/wiki-brain/internal/wiki"
)

type Service struct {
	store         *Store
	llmClient     llm.LLMClient
	unitsIndex    bleve.Index
	pointsIndex   bleve.Index
	outlinesIndex bleve.Index
	cfg           *config.Config
	activationSvc *activation.Service
	evidenceSvc   *evidence.Service
	wikiSvc       *wiki.Service
}

const (
	defaultRerankBatchMaxChars = 8000
	defaultRerankConcurrency   = 2
)

func NewService(store *Store, llmClient llm.LLMClient, unitsIdx, pointsIdx, outlinesIdx bleve.Index, cfg *config.Config, activationSvc *activation.Service, evidenceSvc *evidence.Service, wikiSvc *wiki.Service) *Service {
	return &Service{
		store:         store,
		llmClient:     llmClient,
		unitsIndex:    unitsIdx,
		pointsIndex:   pointsIdx,
		outlinesIndex: outlinesIdx,
		cfg:           cfg,
		activationSvc: activationSvc,
		evidenceSvc:   evidenceSvc,
		wikiSvc:       wikiSvc,
	}
}

// FastPathFallbackEnabled reports whether Answer should redo a failed
// fast-path answer via the slow path (docs/impl/v1/retrieval.md 步骤 6b);
// centralized here since Retrieval owns the retrieval.* config section.
func (s *Service) FastPathFallbackEnabled() bool {
	return s.cfg.Retrieval.FastPathFallback
}

func (s *Service) Retrieve(ctx context.Context, question string) (*EvidenceSet, error) {
	return s.RetrieveWithProgress(ctx, QueryContext{Question: question}, nil)
}

// RetrieveWithProgress dispatches to the activation fast path
// (docs/impl/v1/retrieval.md 步骤 2) unless ForceFull is set or fast_path is
// disabled, falling back to the full MVP pipeline whenever the fast path
// can't be built or Match finds nothing. When fast_path=false the match is
// still performed and its activation_hits are merged into the slow-path
// result — "记录命中日志后仍走慢路径" — so gated rollout can compare hit
// quality without changing behavior.
func (s *Service) RetrieveWithProgress(ctx context.Context, qc QueryContext, progress ProgressFunc) (*EvidenceSet, error) {
	if !qc.ForceFull {
		if wikiES, ok := s.tryWikiAnswer(ctx, qc); ok {
			return wikiES, nil
		}
		fastES, hits, ok := s.tryFastPath(ctx, qc)
		if ok {
			return fastES, nil
		}
		if len(hits) > 0 {
			slowES, err := s.retrieveSlowPath(ctx, qc, progress)
			if err != nil {
				return nil, err
			}
			slowES.ActivationHits = hits
			return slowES, nil
		}
	}
	return s.retrieveSlowPath(ctx, qc, progress)
}

// tryWikiAnswer implements docs/impl/v1/retrieval.md 第 0 层: query the Wiki
// index before the activation fast path (不调 LLM 除非命中分达标), and if the
// hit page can sufficiently answer the question, return a path_type=wiki
// EvidenceSet without ever reaching the fast/slow path. wikiSvc is nil until
// main.go wires it up, matching the doc's "未实现时第 0 层跳过".
func (s *Service) tryWikiAnswer(ctx context.Context, qc QueryContext) (*EvidenceSet, bool) {
	if s.wikiSvc == nil {
		return nil, false
	}
	result, ok, err := s.wikiSvc.TryDirectAnswer(ctx, qc.Question, s.cfg.Retrieval.WikiMinScore)
	if err != nil {
		slog.Warn("wiki direct-answer failed, falling back", "error", err)
		return nil, false
	}
	if !ok {
		return nil, false
	}
	return &EvidenceSet{
		Question:          qc.Question,
		Subject:           qc.Subject,
		Intent:            qc.Intent,
		Audience:          qc.Audience,
		Constraint:        qc.Constraint,
		Path:              "direct",
		PathType:          PathTypeWiki,
		ActivationHits:    []ActivationHit{},
		DirectEvidence:    []Evidence{},
		Supporting:        []Evidence{},
		WikiPageID:        result.PageID,
		CitedPointIDs:     result.CitedPointIDs,
		WikiAnswerContent: result.Content,
	}, true
}

// RetrieveSlowPathWithProgress forces the full MVP pipeline, used by Answer's
// step-6b fallback to redo retrieval after a fast-path answer fails
// (docs/impl/v1/retrieval.md 步骤 6).
func (s *Service) RetrieveSlowPathWithProgress(ctx context.Context, qc QueryContext, progress ProgressFunc) (*EvidenceSet, error) {
	return s.retrieveSlowPath(ctx, qc, progress)
}

// tryFastPath implements docs/impl/v1/retrieval.md 步骤 2. It returns
// (evidenceSet, activationHits, true) on a usable fast-path result; when the
// fast path isn't viable it returns (nil, activationHits, false) — the
// caller falls back to the slow path but keeps activationHits so they still
// end up in the (path_type=full) EvidenceSet for Trace to grade as failures.
func (s *Service) tryFastPath(ctx context.Context, qc QueryContext) (*EvidenceSet, []ActivationHit, bool) {
	if s.activationSvc == nil {
		return nil, nil, false
	}

	matchCfg := activation.MatchConfig{
		MatchMin:         s.cfg.Retrieval.ActivationMatchMin,
		MatchMinFallback: s.cfg.Retrieval.ActivationMatchMinFallback,
		MatchTop:         s.cfg.Retrieval.ActivationMatchTop,
	}
	expandedQuery := session.ExpandedQuery{
		ExpandedQuestion: qc.Question,
		Subject:          qc.Subject,
		Intent:           qc.Intent,
		Audience:         qc.Audience,
		Constraint:       qc.Constraint,
	}
	matches, err := s.activationSvc.Match(expandedQuery, matchCfg)
	if err != nil {
		slog.Warn("retrieval: activation match failed, falling back to slow path", "error", err)
		return nil, nil, false
	}
	if len(matches) == 0 {
		return nil, nil, false
	}

	activationHits := make([]ActivationHit, len(matches))
	linkIDs := make([]string, len(matches))
	pointIDs := make([]string, len(matches))
	for i, m := range matches {
		activationHits[i] = ActivationHit{LinkID: m.Link.LinkID, PointID: m.Link.PointID, MatchScore: m.Score}
		linkIDs[i] = m.Link.LinkID
		pointIDs[i] = m.Link.PointID
	}
	slog.Info("retrieval: activation layer matched", "link_count", len(matches), "link_ids", linkIDs)

	// Async, non-blocking — Retrieval's own failure/success handling doesn't
	// depend on this having completed.
	go func() {
		if err := s.activationSvc.TouchLastUsed(linkIDs); err != nil {
			slog.Warn("retrieval: touch last used failed", "error", err)
		}
	}()

	if !s.cfg.Retrieval.FastPath {
		return nil, activationHits, false
	}

	hits, err := s.store.GetCurrentUnitsByPointIDs(pointIDs)
	if err != nil {
		slog.Warn("retrieval: fast path unit lookup failed, falling back to slow path", "error", err)
		return nil, activationHits, false
	}
	if len(hits) == 0 {
		// Match already filters to verified+current, so this shouldn't happen;
		// treat as a fallback rather than an error.
		slog.Warn("retrieval: fast path found no current KU for matched links, falling back", "link_ids", linkIDs)
		return nil, activationHits, false
	}

	seen := make(map[string]bool, len(hits))
	var direct []candidate
	for _, h := range hits {
		if seen[h.UnitID] {
			continue
		}
		seen[h.UnitID] = true
		direct = append(direct, candidate{
			unitID:      h.UnitID,
			pointID:     h.PointID,
			sourceID:    h.SourceID,
			lineStart:   h.LineStart,
			lineEnd:     h.LineEnd,
			sourcePaths: []string{"direct"},
		})
	}

	allCandidates, conflictCandidates, err := s.kpnExpand(direct)
	if err != nil {
		slog.Warn("retrieval: fast path kpn expand failed, falling back to slow path", "error", err)
		return nil, activationHits, false
	}

	var directCands, supportingCands []candidate
	for _, c := range allCandidates {
		switch c.sourcePaths[0] {
		case "direct":
			directCands = append(directCands, c)
		case "supporting":
			supportingCands = append(supportingCands, c)
		}
	}

	es, err := s.buildEvidenceSet(ctx, qc.Question, qc.Subject, qc.Intent, qc.Audience, qc.Constraint, "short", directCands, supportingCands, conflictCandidates, nil)
	if err != nil {
		slog.Warn("retrieval: fast path build evidence set failed, falling back to slow path", "error", err)
		return nil, activationHits, false
	}

	// "direct 候选挖掘后片段全部为空且整段回退也为空（KU 正文读取失败等异常）
	// → 视为快路径失败": Evidence Mining always keeps at least a whole-segment
	// item for a direct candidate (mined=false) unless its KU content couldn't
	// even be read, so an empty DirectEvidence here means that read failed.
	if len(es.DirectEvidence) == 0 {
		slog.Warn("retrieval: fast path produced no direct evidence, falling back to slow path", "link_ids", linkIDs)
		return nil, activationHits, false
	}

	es.PathType = PathTypeFast
	es.ActivationHits = activationHits
	slog.Info("retrieval: fast path evidence built",
		"direct", len(es.DirectEvidence), "supporting", len(es.Supporting), "link_ids", linkIDs)
	return es, activationHits, true
}

func (s *Service) retrieveSlowPath(ctx context.Context, qc QueryContext, progress ProgressFunc) (*EvidenceSet, error) {
	question := qc.Question
	emit := func(phase, status, detail string, dur int64) {
		if progress != nil {
			progress(ProgressEvent{Phase: phase, Status: status, Detail: detail, Duration: dur})
		}
	}

	// Step 2-3: 知识点激活（Domain pre-filter + Source semantic filter）
	emit("activation", "start", "", 0)
	activationStart := time.Now()

	candidateSources, err := s.domainPreFilter(ctx, question)
	if err != nil {
		emit("activation", "error", err.Error(), time.Since(activationStart).Milliseconds())
		return nil, fmt.Errorf("retrieval: domain pre-filter: %w", err)
	}
	slog.Info("retrieval: step2 domain pre-filter done", "candidates", len(candidateSources))

	filteredSources, err := s.sourceSemanticFilter(ctx, qc, candidateSources)
	if err != nil {
		emit("activation", "error", err.Error(), time.Since(activationStart).Milliseconds())
		return nil, fmt.Errorf("retrieval: source filter: %w", err)
	}

	sourceIDs := make([]string, len(filteredSources))
	for i, src := range filteredSources {
		sourceIDs[i] = src.SourceID
	}
	slog.Info("retrieval: step3 source filter done", "sources", sourceIDs)
	emit("activation", "done", fmt.Sprintf("%d 个来源", len(sourceIDs)), time.Since(activationStart).Milliseconds())

	// Step 4: 目录结构检索（Outline recall）
	emit("outline", "start", "", 0)
	outlineStart := time.Now()
	outlineCandidates, err := s.outlineRecall(ctx, qc, sourceIDs)
	if err != nil {
		emit("outline", "error", err.Error(), time.Since(outlineStart).Milliseconds())
		return nil, fmt.Errorf("retrieval: outline recall: %w", err)
	}
	slog.Info("retrieval: step4 outline recall done", "candidates", len(outlineCandidates))
	emit("outline", "done", fmt.Sprintf("%d 条", len(outlineCandidates)), time.Since(outlineStart).Milliseconds())

	// Step 5: 全文检索（FTS recall）
	emit("fts", "start", "", 0)
	ftsStart := time.Now()
	ftsCandidates, err := s.ftsRecall(question, sourceIDs)
	if err != nil {
		emit("fts", "error", err.Error(), time.Since(ftsStart).Milliseconds())
		return nil, fmt.Errorf("retrieval: fts recall: %w", err)
	}
	slog.Info("retrieval: step5 fts recall done", "candidates", len(ftsCandidates))
	emit("fts", "done", fmt.Sprintf("%d 条", len(ftsCandidates)), time.Since(ftsStart).Milliseconds())

	// Step 6: RRF merge
	merged := s.rrfMerge(outlineCandidates, ftsCandidates)
	slog.Info("retrieval: step6 rrf merge done", "merged", len(merged))

	if len(merged) == 0 {
		emit("rerank", "done", "无候选", 0)
		return &EvidenceSet{
			Question:       question,
			Subject:        qc.Subject,
			Intent:         qc.Intent,
			Audience:       qc.Audience,
			Constraint:     qc.Constraint,
			Path:           "deep",
			PathType:       PathTypeFull,
			ActivationHits: []ActivationHit{},
			DirectEvidence: []Evidence{},
			Supporting:     []Evidence{},
			Conflicts:      nil,
		}, nil
	}

	// Step 7: 证据分类（LLM Rerank）
	emit("rerank", "start", fmt.Sprintf("%d 条候选", len(merged)), 0)
	rerankStart := time.Now()
	reranked, err := s.rerank(ctx, qc, merged)
	if err != nil {
		emit("rerank", "error", err.Error(), time.Since(rerankStart).Milliseconds())
		return nil, fmt.Errorf("retrieval: rerank: %w", err)
	}
	slog.Info("retrieval: step7 rerank done", "kept", len(reranked))

	// Step 8: KPN expansion
	reranked, conflictCandidates, err := s.kpnExpand(reranked)
	if err != nil {
		return nil, fmt.Errorf("retrieval: kpn expand: %w", err)
	}

	// Step 9: Sufficiency check
	path := "deep"
	var direct, supporting []candidate
	for _, c := range reranked {
		switch c.sourcePaths[0] {
		case "direct":
			direct = append(direct, c)
		case "supporting":
			supporting = append(supporting, c)
		}
	}
	if len(direct) > 0 {
		path = "short"
	}
	slog.Info("retrieval: step9 sufficiency", "path", path, "direct", len(direct), "supporting", len(supporting), "conflicts", len(conflictCandidates))
	emit("rerank", "done", fmt.Sprintf("%d 直接 · %d 补充", len(direct), len(supporting)), time.Since(rerankStart).Milliseconds())

	// Step 10: Build EvidenceSet
	es, err := s.buildEvidenceSet(ctx, question, qc.Subject, qc.Intent, qc.Audience, qc.Constraint, path, direct, supporting, conflictCandidates, progress)
	if err != nil {
		return nil, fmt.Errorf("retrieval: build evidence set: %w", err)
	}
	return es, nil
}

// Step 2: Domain pre-filter
func (s *Service) domainPreFilter(ctx context.Context, question string) ([]SourceInfo, error) {
	domains, err := s.store.ListDomains()
	if err != nil {
		return nil, err
	}
	if len(domains) == 0 {
		return s.store.ListAllSources()
	}

	var domainList strings.Builder
	for _, d := range domains {
		fmt.Fprintf(&domainList, "[%s] %s：%s\n", d.DomainID, d.Name, d.Description)
	}

	resp, err := s.llmClient.CompleteJSON(ctx, "question_domain_match.md", map[string]string{
		"question":    question,
		"domain_list": domainList.String(),
	}, "classification")
	if err != nil {
		slog.Warn("retrieval: domain match failed, skipping", "error", err)
		return s.store.ListAllSources()
	}

	var result struct {
		DomainIDs []string `json:"domain_ids"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		slog.Warn("retrieval: domain match parse failed, skipping", "error", err)
		return s.store.ListAllSources()
	}

	if len(result.DomainIDs) == 0 {
		return s.store.ListAllSources()
	}

	sources, err := s.store.ListSourcesByDomainIDs(result.DomainIDs)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		slog.Warn("retrieval: domain match matched zero sources, falling back to all sources", "domain_ids", result.DomainIDs)
		return s.store.ListAllSources()
	}
	return sources, nil
}

// Step 3: Source semantic filter
func (s *Service) sourceSemanticFilter(ctx context.Context, qc QueryContext, candidates []SourceInfo) ([]SourceInfo, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	var sourceList strings.Builder
	for _, src := range candidates {
		summary := ""
		if src.Summary.Valid {
			summary = src.Summary.String
		}
		if summary != "" {
			fmt.Fprintf(&sourceList, "[%s] 标题：%s / 概述：%s\n", src.SourceID, src.Title, summary)
		} else {
			fmt.Fprintf(&sourceList, "[%s] 标题：%s\n", src.SourceID, src.Title)
		}
	}

	subject := qc.Subject
	if subject == "" {
		subject = "（未提取）"
	}
	intent := qc.Intent
	if intent == "" {
		intent = "（未提取）"
	}

	resp, err := s.llmClient.CompleteJSON(ctx, "source_filter.md", map[string]string{
		"question":    qc.Question,
		"subject":     subject,
		"intent":      intent,
		"source_list": sourceList.String(),
	}, "classification")
	if err != nil {
		slog.Warn("retrieval: source filter failed, using all candidates", "error", err)
		return candidates, nil
	}

	var result struct {
		SourceIDs []string `json:"source_ids"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		slog.Warn("retrieval: source filter parse failed, using all candidates", "error", err)
		return candidates, nil
	}

	if len(result.SourceIDs) == 0 {
		return candidates, nil
	}

	idSet := make(map[string]bool, len(result.SourceIDs))
	for _, id := range result.SourceIDs {
		idSet[id] = true
	}
	var filtered []SourceInfo
	for _, src := range candidates {
		if idSet[src.SourceID] {
			filtered = append(filtered, src)
		}
	}
	if len(filtered) == 0 {
		return candidates, nil
	}
	return filtered, nil
}

// Step 4: Outline recall
func (s *Service) outlineRecall(ctx context.Context, qc QueryContext, sourceIDs []string) ([]candidate, error) {
	if len(sourceIDs) == 0 {
		return nil, nil
	}

	// 目录召回按核心主题+意图整体匹配，而非裸问题文本——问题需经 session parser
	// 解析才完整（如指代消解），裸问题字面匹配容易因同义/近义关键词误召回
	// 场景不同的章节（如"实施"既出现在实施考核场景，也出现在销售代理场景）。
	matchText := strings.TrimSpace(qc.Subject + " " + qc.Intent)
	if matchText == "" {
		matchText = qc.Question
	}

	threshold := s.cfg.Retrieval.OutlineFTSMinScore
	if threshold <= 0 {
		threshold = 0.5
	}

	// 4.1 FTS on outlines index
	type sourceScore struct {
		maxScore   float64
		outlineIDs []string
	}
	scoreBySource := make(map[string]*sourceScore)
	for _, sid := range sourceIDs {
		scoreBySource[sid] = &sourceScore{}
	}

	q := bleve.NewMatchQuery(matchText)
	searchReq := bleve.NewSearchRequest(q)
	searchReq.Size = 100
	searchReq.Fields = []string{"source_id", "outline_id"}

	results, err := s.outlinesIndex.Search(searchReq)
	if err != nil {
		slog.Warn("retrieval: outline fts failed", "error", err)
	} else {
		for _, hit := range results.Hits {
			sid, _ := hit.Fields["source_id"].(string)
			ss, ok := scoreBySource[sid]
			if !ok {
				continue
			}
			if hit.Score > ss.maxScore {
				ss.maxScore = hit.Score
			}
			ss.outlineIDs = append(ss.outlineIDs, hit.ID)
		}
	}

	// Collect FTS hits from sources above threshold
	var ftsOutlineIDs []string
	var lowScoreSources []string
	for sid, ss := range scoreBySource {
		if ss.maxScore >= threshold {
			ftsOutlineIDs = append(ftsOutlineIDs, ss.outlineIDs...)
		} else {
			lowScoreSources = append(lowScoreSources, sid)
		}
	}

	// 4.2 LLM fallback for low-score sources
	if len(lowScoreSources) > 0 {
		llmOutlineIDs, err := s.outlineLLMFallback(ctx, qc, lowScoreSources)
		if err != nil {
			slog.Warn("retrieval: outline llm fallback error", "error", err)
		} else {
			ftsOutlineIDs = append(ftsOutlineIDs, llmOutlineIDs...)
		}
	}

	if len(ftsOutlineIDs) == 0 {
		return nil, nil
	}

	// Expand to children and get units
	var allOutlineIDs []string
	sourceIDSet := make(map[string]bool)
	for _, sid := range sourceIDs {
		sourceIDSet[sid] = true
	}

	// Group outline IDs by source for child expansion
	outlinesBySource := make(map[string][]string)
	outlines, err := s.store.GetOutlinesBySourceIDs(sourceIDs)
	if err != nil {
		return nil, err
	}
	outlineSourceMap := make(map[string]string)
	for _, o := range outlines {
		outlineSourceMap[o.OutlineID] = o.SourceID
	}
	for _, oid := range ftsOutlineIDs {
		if sid, ok := outlineSourceMap[oid]; ok {
			outlinesBySource[sid] = append(outlinesBySource[sid], oid)
		}
	}

	for sid, oids := range outlinesBySource {
		children, err := s.store.GetChildOutlineIDs(oids, sid)
		if err != nil {
			return nil, err
		}
		allOutlineIDs = append(allOutlineIDs, children...)
	}

	units, err := s.store.GetUnitsByOutlineIDs(allOutlineIDs)
	if err != nil {
		return nil, err
	}

	var candidates []candidate
	for _, u := range units {
		pointID, err := s.store.GetFirstPointByUnitID(u.UnitID)
		if err != nil {
			slog.Warn("retrieval: outline recall unit has no KP, skipping", "unit_id", u.UnitID)
			continue
		}
		candidates = append(candidates, candidate{
			unitID:      u.UnitID,
			pointID:     pointID,
			sourceID:    u.SourceID,
			lineStart:   u.LineStart,
			lineEnd:     u.LineEnd,
			score:       1.0,
			sourcePaths: []string{"outline"},
		})
	}
	return candidates, nil
}

func (s *Service) outlineLLMFallback(ctx context.Context, qc QueryContext, sourceIDs []string) ([]string, error) {
	outlines, err := s.store.GetOutlinesBySourceIDs(sourceIDs)
	if err != nil {
		return nil, err
	}

	// Group outlines by source
	bySource := make(map[string][]OutlineInfo)
	for _, o := range outlines {
		bySource[o.SourceID] = append(bySource[o.SourceID], o)
	}

	type result struct {
		outlineIDs []string
		err        error
	}

	subject := qc.Subject
	if subject == "" {
		subject = "（未提取）"
	}
	intent := qc.Intent
	if intent == "" {
		intent = "（未提取）"
	}

	var mu sync.Mutex
	var allIDs []string
	var wg sync.WaitGroup

	for sid, sourceOutlines := range bySource {
		_ = sid
		wg.Add(1)
		go func(ols []OutlineInfo) {
			defer wg.Done()
			var outlineList strings.Builder
			for _, o := range ols {
				indent := strings.Repeat("  ", o.Level-1)
				line := fmt.Sprintf("[%s] %s%s", o.OutlineID, indent, o.Title)
				if o.Summary.Valid && o.Summary.String != "" {
					line += " / 关键词：" + o.Summary.String
				}
				outlineList.WriteString(line + "\n")
			}

			resp, err := s.llmClient.CompleteJSON(ctx, "outline_filter.md", map[string]string{
				"question":     qc.Question,
				"subject":      subject,
				"intent":       intent,
				"outline_list": outlineList.String(),
			}, "classification")
			if err != nil {
				slog.Warn("retrieval: outline llm fallback failed for source", "error", err)
				return
			}

			var parsed struct {
				OutlineIDs []string `json:"outline_ids"`
			}
			if err := json.Unmarshal(resp, &parsed); err != nil {
				slog.Warn("retrieval: outline llm parse failed", "error", err)
				return
			}

			mu.Lock()
			allIDs = append(allIDs, parsed.OutlineIDs...)
			mu.Unlock()
		}(sourceOutlines)
	}
	wg.Wait()
	return allIDs, nil
}

// Step 5: FTS recall
func (s *Service) ftsRecall(question string, sourceIDs []string) ([]candidate, error) {
	if len(sourceIDs) == 0 {
		return nil, nil
	}

	sourceIDSet := make(map[string]bool, len(sourceIDs))
	for _, id := range sourceIDs {
		sourceIDSet[id] = true
	}

	// unitID → candidate
	unitMap := make(map[string]*candidate)

	// Search units index
	uq := lifecycleCurrentQuery(bleve.NewMatchQuery(question))
	uReq := bleve.NewSearchRequest(uq)
	uReq.Size = 100
	uReq.Fields = []string{"unit_id", "source_id", "line_start", "line_end"}

	uResults, err := s.unitsIndex.Search(uReq)
	if err != nil {
		slog.Warn("retrieval: units fts failed", "error", err)
	} else {
		for _, hit := range uResults.Hits {
			sid, _ := hit.Fields["source_id"].(string)
			if !sourceIDSet[sid] {
				continue
			}
			uid := hit.ID
			lineStart := intFromField(hit.Fields["line_start"])
			lineEnd := intFromField(hit.Fields["line_end"])

			if existing, ok := unitMap[uid]; ok {
				if hit.Score > existing.score {
					existing.score = hit.Score
				}
			} else {
				unitMap[uid] = &candidate{
					unitID:      uid,
					sourceID:    sid,
					lineStart:   lineStart,
					lineEnd:     lineEnd,
					score:       hit.Score,
					sourcePaths: []string{"fts"},
				}
			}
		}
	}

	// Search points index
	pq := lifecycleCurrentQuery(bleve.NewMatchQuery(question))
	pReq := bleve.NewSearchRequest(pq)
	pReq.Size = 100
	pReq.Fields = []string{"point_id", "unit_id", "source_id"}

	// Track best point per unit from points path
	type pointHit struct {
		pointID string
		score   float64
	}
	bestPointPerUnit := make(map[string]pointHit)

	pResults, err := s.pointsIndex.Search(pReq)
	if err != nil {
		slog.Warn("retrieval: points fts failed", "error", err)
	} else {
		for _, hit := range pResults.Hits {
			sid, _ := hit.Fields["source_id"].(string)
			if !sourceIDSet[sid] {
				continue
			}
			pointID := hit.ID
			unitID, err := s.store.GetPointUnitID(pointID)
			if err != nil {
				slog.Warn("retrieval: point unit lookup failed", "point_id", pointID, "error", err)
				continue
			}

			if existing, ok := bestPointPerUnit[unitID]; !ok || hit.Score > existing.score {
				bestPointPerUnit[unitID] = pointHit{pointID: pointID, score: hit.Score}
			}

			if existing, ok := unitMap[unitID]; ok {
				if hit.Score > existing.score {
					existing.score = hit.Score
				}
			} else {
				u, err := s.store.GetUnitByID(unitID)
				if err != nil {
					slog.Warn("retrieval: unit lookup failed", "unit_id", unitID, "error", err)
					continue
				}
				unitMap[unitID] = &candidate{
					unitID:      unitID,
					sourceID:    u.SourceID,
					lineStart:   u.LineStart,
					lineEnd:     u.LineEnd,
					score:       hit.Score,
					sourcePaths: []string{"fts"},
				}
			}
		}
	}

	// Assign point_ids
	var result []candidate
	for uid, c := range unitMap {
		if ph, ok := bestPointPerUnit[uid]; ok {
			c.pointID = ph.pointID
		} else {
			pid, err := s.store.GetFirstPointByUnitID(uid)
			if err != nil {
				slog.Warn("retrieval: fts recall unit has no KP, skipping", "unit_id", uid)
				continue
			}
			c.pointID = pid
		}
		result = append(result, *c)
	}
	return result, nil
}

// Step 6: RRF merge
func (s *Service) rrfMerge(outlineCandidates, ftsCandidates []candidate) []candidate {
	const k = 60

	// Rank each list by score descending
	sort.Slice(outlineCandidates, func(i, j int) bool {
		return outlineCandidates[i].score > outlineCandidates[j].score
	})
	sort.Slice(ftsCandidates, func(i, j int) bool {
		return ftsCandidates[i].score > ftsCandidates[j].score
	})

	type mergedCandidate struct {
		candidate
		rrfScore float64
		paths    map[string]bool
	}
	merged := make(map[string]*mergedCandidate)

	addRanked := func(candidates []candidate, pathName string) {
		for rank, c := range candidates {
			rrfScore := 1.0 / float64(k+rank+1)
			if m, ok := merged[c.unitID]; ok {
				m.rrfScore += rrfScore
				m.paths[pathName] = true
				if c.pointID != "" && m.pointID == "" {
					m.pointID = c.pointID
				}
			} else {
				merged[c.unitID] = &mergedCandidate{
					candidate: c,
					rrfScore:  rrfScore,
					paths:     map[string]bool{pathName: true},
				}
			}
		}
	}

	addRanked(outlineCandidates, "outline")
	addRanked(ftsCandidates, "fts")

	var result []candidate
	for _, m := range merged {
		paths := make([]string, 0, len(m.paths))
		for p := range m.paths {
			paths = append(paths, p)
		}
		m.candidate.score = m.rrfScore
		m.candidate.sourcePaths = paths
		result = append(result, m.candidate)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].score > result[j].score
	})

	topN := s.cfg.Retrieval.RerankTopN
	if topN <= 0 {
		topN = 20
	}
	if len(result) > topN {
		result = result[:topN]
	}

	return result
}

// Step 7: LLM Rerank
func (s *Service) rerank(ctx context.Context, qc QueryContext, candidates []candidate) ([]candidate, error) {
	// Re-check KU lifecycle right before content is sliced for the LLM — recall
	// happened moments earlier via Bleve, which can lag a DB lifecycle change
	// (docs/impl/v1/retrieval.md 步骤 5, "防扫描间隙状态变更").
	candidates = s.filterCurrentUnits(candidates)

	// Assign candidate_ids
	for i := range candidates {
		candidates[i].candidateID = fmt.Sprintf("c%d", i+1)
	}

	titleCache := make(map[string]string)
	for _, c := range candidates {
		if _, ok := titleCache[c.sourceID]; !ok {
			title, err := s.store.GetSourceTitle(c.sourceID)
			if err != nil {
				slog.Warn("retrieval: rerank get source title failed", "source_id", c.sourceID, "error", err)
				title = c.sourceID
			}
			titleCache[c.sourceID] = title
		}
	}

	prepared := make([]rerankCandidateContent, 0, len(candidates))
	for _, c := range candidates {
		content, err := s.readUnitContent(c.sourceID, c.lineStart, c.lineEnd)
		if err != nil {
			slog.Warn("retrieval: rerank read content failed", "unit_id", c.unitID, "error", err)
			continue
		}
		prepared = append(prepared, rerankCandidateContent{candidate: c, content: content})
	}
	batches := splitRerankBatches(prepared, s.rerankBatchMaxChars())

	subject := qc.Subject
	if subject == "" {
		subject = "（未提取）"
	}
	intent := qc.Intent
	if intent == "" {
		intent = "（未提取）"
	}
	audience := qc.Audience
	if audience == "" {
		audience = "（未提取）"
	}
	constraint := qc.Constraint
	if constraint == "" {
		constraint = "（无）"
	}

	roles, err := s.rerankBatches(ctx, qc, subject, intent, audience, constraint, titleCache, batches)
	if err != nil {
		return nil, err
	}

	var kept []candidate
	for _, c := range candidates {
		role, ok := roles[c.candidateID]
		if !ok || role == "irrelevant" {
			continue
		}
		c.sourcePaths = []string{role}
		kept = append(kept, c)
	}
	return kept, nil
}

func (s *Service) rerankBatches(ctx context.Context, qc QueryContext, subject, intent, audience, constraint string, titleCache map[string]string, batches [][]rerankCandidateContent) (map[string]string, error) {
	if len(batches) == 0 {
		return nil, nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	concurrency := s.rerankConcurrency()
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	roles := make(map[string]string)
	var firstErr error
	var errOnce sync.Once

	for _, batch := range batches {
		batch := batch
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			batchRoles, err := s.rerankBatch(ctx, qc, subject, intent, audience, constraint, titleCache, batch)
			if err != nil {
				errOnce.Do(func() {
					firstErr = err
					cancel()
				})
				return
			}
			mu.Lock()
			for cid, role := range batchRoles {
				roles[cid] = role
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return roles, nil
}

func (s *Service) rerankBatch(ctx context.Context, qc QueryContext, subject, intent, audience, constraint string, titleCache map[string]string, batch []rerankCandidateContent) (map[string]string, error) {
	candidatesText := formatRerankCandidates(batch, titleCache)
	resp, err := s.llmClient.CompleteJSON(ctx, "rerank_extract.md", map[string]string{
		"question":   qc.Question,
		"subject":    subject,
		"intent":     intent,
		"audience":   audience,
		"constraint": constraint,
		"candidates": candidatesText,
	}, "classification")
	if err != nil {
		return nil, fmt.Errorf("retrieval: rerank llm: %w", err)
	}

	var extraction rerankExtractionResult
	if err := json.Unmarshal(resp, &extraction); err != nil {
		return nil, fmt.Errorf("retrieval: rerank parse: %w", err)
	}

	// Validate
	cidSet := make(map[string]bool, len(batch))
	for _, item := range batch {
		cidSet[item.candidateID] = true
	}
	extractedByID := make(map[string]rerankExtractedEvidence, len(extraction.Results))
	for _, item := range extraction.Results {
		if !cidSet[item.CandidateID] {
			return nil, fmt.Errorf("retrieval: rerank returned unknown candidate_id: %s", item.CandidateID)
		}
		extractedByID[item.CandidateID] = item
	}

	judgeInputs := make([]rerankJudgeCandidate, 0, len(batch))
	for _, item := range batch {
		extracted, ok := extractedByID[item.candidateID]
		if !ok {
			continue
		}
		judgeInputs = append(judgeInputs, rerankJudgeCandidate{
			CandidateID:  item.candidateID,
			SourceTitle:  titleCache[item.sourceID],
			SourceTheme:  extracted.SourceTheme,
			ContentTheme: extracted.ContentTheme,
			Intent:       extracted.Intent,
			Object:       extracted.Object,
			Scope:        extracted.Scope,
			KeyFacts:     extracted.KeyFacts,
		})
	}

	roles, err := s.judgeExtractedEvidence(ctx, qc, subject, intent, audience, constraint, judgeInputs)
	if err != nil {
		return nil, err
	}

	return roles, nil
}

func (s *Service) judgeExtractedEvidence(ctx context.Context, qc QueryContext, subject, intent, audience, constraint string, candidates []rerankJudgeCandidate) (map[string]string, error) {
	payload, err := json.MarshalIndent(candidates, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("retrieval: rerank judge payload: %w", err)
	}
	resp, err := s.llmClient.CompleteJSON(ctx, "rerank_judge.md", map[string]string{
		"question":   qc.Question,
		"subject":    subject,
		"intent":     intent,
		"audience":   audience,
		"constraint": constraint,
		"candidates": string(payload),
	}, "classification")
	if err != nil {
		return nil, fmt.Errorf("retrieval: rerank judge llm: %w", err)
	}

	var result rerankJudgeResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("retrieval: rerank judge parse: %w", err)
	}

	cidSet := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		cidSet[c.CandidateID] = true
	}
	roles := make(map[string]string, len(result.Results))
	for _, r := range result.Results {
		if !cidSet[r.CandidateID] {
			return nil, fmt.Errorf("retrieval: rerank judge returned unknown candidate_id: %s", r.CandidateID)
		}
		if r.Role != "direct" && r.Role != "supporting" && r.Role != "irrelevant" {
			return nil, fmt.Errorf("retrieval: rerank judge invalid role: %s", r.Role)
		}
		roles[r.CandidateID] = r.Role
	}
	return roles, nil
}

type rerankCandidateContent struct {
	candidate
	content string
}

func (s *Service) rerankBatchMaxChars() int {
	if s.cfg != nil && s.cfg.Retrieval.RerankBatchMaxChars > 0 {
		return s.cfg.Retrieval.RerankBatchMaxChars
	}
	return defaultRerankBatchMaxChars
}

func (s *Service) rerankConcurrency() int {
	if s.cfg != nil && s.cfg.Retrieval.RerankConcurrency > 0 {
		return s.cfg.Retrieval.RerankConcurrency
	}
	return defaultRerankConcurrency
}

func splitRerankBatches(candidates []rerankCandidateContent, maxChars int) [][]rerankCandidateContent {
	if len(candidates) == 0 {
		return nil
	}
	if maxChars <= 0 {
		maxChars = defaultRerankBatchMaxChars
	}

	var batches [][]rerankCandidateContent
	var current []rerankCandidateContent
	currentChars := 0
	for _, c := range candidates {
		itemChars := len(c.content)
		if len(current) > 0 && currentChars+itemChars > maxChars {
			batches = append(batches, current)
			current = nil
			currentChars = 0
		}
		current = append(current, c)
		currentChars += itemChars
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}

func formatRerankCandidates(candidates []rerankCandidateContent, titleCache map[string]string) string {
	// Group each batch by source document, so the LLM can see which
	// policy/document each piece belongs to without receiving every candidate.
	var sourceOrder []string
	bySource := make(map[string][]rerankCandidateContent)
	for _, c := range candidates {
		if _, ok := bySource[c.sourceID]; !ok {
			sourceOrder = append(sourceOrder, c.sourceID)
		}
		bySource[c.sourceID] = append(bySource[c.sourceID], c)
	}

	var out strings.Builder
	for _, sourceID := range sourceOrder {
		title := titleCache[sourceID]
		if title == "" {
			title = sourceID
		}
		fmt.Fprintf(&out, "【来源文档：%s】\n", title)
		for _, c := range bySource[sourceID] {
			fmt.Fprintf(&out, "[%s] %s\n\n", c.candidateID, c.content)
		}
	}
	return out.String()
}

type rerankExtractionResult struct {
	Results []rerankExtractedEvidence `json:"results"`
}

type rerankExtractedEvidence struct {
	CandidateID  string   `json:"candidate_id"`
	SourceTheme  string   `json:"source_theme"`
	ContentTheme string   `json:"content_theme"`
	Intent       string   `json:"intent"`
	Object       string   `json:"object"`
	Scope        string   `json:"scope"`
	KeyFacts     []string `json:"key_facts"`
}

type rerankJudgeCandidate struct {
	CandidateID  string   `json:"candidate_id"`
	SourceTitle  string   `json:"source_title"`
	SourceTheme  string   `json:"source_theme"`
	ContentTheme string   `json:"content_theme"`
	Intent       string   `json:"intent"`
	Object       string   `json:"object"`
	Scope        string   `json:"scope"`
	KeyFacts     []string `json:"key_facts"`
}

type rerankJudgeResult struct {
	Results []struct {
		CandidateID string `json:"candidate_id"`
		Role        string `json:"role"`
		Analysis    string `json:"analysis"`
	} `json:"results"`
}

// Step 8: KPN expansion
// Returns updated candidates (with supporting neighbors) and conflict candidates separately.
func (s *Service) kpnExpand(candidates []candidate) ([]candidate, []candidate, error) {
	var directUnitIDs []string
	for _, c := range candidates {
		if len(c.sourcePaths) > 0 && c.sourcePaths[0] == "direct" {
			directUnitIDs = append(directUnitIDs, c.unitID)
		}
	}
	if len(directUnitIDs) == 0 {
		return candidates, nil, nil
	}

	points, err := s.store.GetPointsByUnitIDs(directUnitIDs)
	if err != nil {
		return nil, nil, err
	}
	seedPointIDs := make([]string, len(points))
	for i, p := range points {
		seedPointIDs[i] = p.PointID
	}

	seedSet := make(map[string]bool, len(seedPointIDs))
	for _, pid := range seedPointIDs {
		seedSet[pid] = true
	}

	existingUnits := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		existingUnits[c.unitID] = true
	}

	// Non-contradicts neighbors → supporting
	neighbors, err := s.store.GetKPNNeighbors(seedPointIDs)
	if err != nil {
		return nil, nil, err
	}

	for _, n := range neighbors {
		if seedSet[n.NeighborPointID] || existingUnits[n.UnitID] {
			continue
		}
		existingUnits[n.UnitID] = true

		u, err := s.store.GetUnitByID(n.UnitID)
		if err != nil {
			slog.Warn("retrieval: kpn expand unit lookup failed", "unit_id", n.UnitID, "error", err)
			continue
		}

		candidates = append(candidates, candidate{
			unitID:      n.UnitID,
			pointID:     n.NeighborPointID,
			sourceID:    u.SourceID,
			lineStart:   u.LineStart,
			lineEnd:     u.LineEnd,
			sourcePaths: []string{"supporting"},
			origin:      OriginKPNExpansion,
		})
	}

	// Contradicts neighbors → conflicts (separate, not mixed into candidates)
	conflicts, err := s.store.GetKPNConflicts(seedPointIDs)
	if err != nil {
		return nil, nil, err
	}

	var conflictCandidates []candidate
	for _, n := range conflicts {
		if seedSet[n.NeighborPointID] {
			continue
		}

		u, err := s.store.GetUnitByID(n.UnitID)
		if err != nil {
			slog.Warn("retrieval: kpn conflict unit lookup failed", "unit_id", n.UnitID, "error", err)
			continue
		}

		conflictCandidates = append(conflictCandidates, candidate{
			unitID:    n.UnitID,
			pointID:   n.NeighborPointID,
			sourceID:  u.SourceID,
			lineStart: u.LineStart,
			lineEnd:   u.LineEnd,
		})
	}

	if len(conflictCandidates) > 0 {
		slog.Info("retrieval: kpn found contradicts", "count", len(conflictCandidates))
	}

	return candidates, conflictCandidates, nil
}

// Step 10: Build EvidenceSet
// buildEvidenceSet reads each candidate's KU text, runs it through the
// Evidence Mining module (docs/impl/v1/evidence.md — fragment-level
// EvidenceItems when mining succeeds, whole-segment passthrough with
// mined=false when it doesn't), and assembles the resulting items into the
// final EvidenceSet (docs/impl/v1/retrieval.md 步骤 3-4). Mining runs once
// here for both the fast and slow paths, since both funnel through this
// function.
func (s *Service) buildEvidenceSet(ctx context.Context, question, subject, intent, audience, constraint, path string, direct, supporting, conflicts []candidate, progress ProgressFunc) (*EvidenceSet, error) {
	emit := func(phase, status, detail string, dur int64) {
		if progress != nil {
			progress(ProgressEvent{Phase: phase, Status: status, Detail: detail, Duration: dur})
		}
	}

	es := &EvidenceSet{
		Question:       question,
		Subject:        subject,
		Intent:         intent,
		Audience:       audience,
		Constraint:     constraint,
		Path:           path,
		PathType:       PathTypeFull,
		ActivationHits: []ActivationHit{},
		DirectEvidence: []Evidence{},
		Supporting:     []Evidence{},
	}

	var items []evidence.EvidenceItem
	appendItems := func(cands []candidate, role string) {
		for _, c := range cands {
			content, err := s.readUnitContent(c.sourceID, c.lineStart, c.lineEnd)
			if err != nil {
				slog.Warn("retrieval: read candidate content failed", "unit_id", c.unitID, "role", role, "error", err)
				continue
			}
			origin := c.origin
			if origin == "" {
				origin = OriginRerank
			}
			items = append(items, evidence.EvidenceItem{
				UnitID: c.unitID, PointID: c.pointID, SourceID: c.sourceID,
				LineStart: c.lineStart, LineEnd: c.lineEnd,
				Content: content, Role: role, Origin: origin,
			})
		}
	}
	appendItems(direct, evidence.RoleDirect)
	appendItems(supporting, evidence.RoleSupporting)

	emit("evidence", "start", fmt.Sprintf("%d 条候选", len(items)), 0)
	evidenceStart := time.Now()
	mined := items
	if s.evidenceSvc != nil {
		mined = s.evidenceSvc.Mine(ctx, question, subject, intent, items)
	}
	minedCount := 0
	for _, item := range mined {
		if item.Mined {
			minedCount++
		}
	}
	emit("evidence", "done", fmt.Sprintf("%d/%d 条片段化", minedCount, len(mined)), time.Since(evidenceStart).Milliseconds())

	for _, item := range mined {
		ref := SourceRef{SourceID: item.SourceID, LineStart: item.LineStart, LineEnd: item.LineEnd}
		refJSON, _ := json.Marshal(ref)
		ev := Evidence{
			FactID:    uuid.New().String(),
			UnitID:    item.UnitID,
			PointID:   item.PointID,
			Content:   item.Content,
			SourceRef: refJSON,
			Role:      item.Role,
			Origin:    item.Origin,
			Mined:     item.Mined,
		}
		switch item.Role {
		case evidence.RoleDirect:
			es.DirectEvidence = append(es.DirectEvidence, ev)
		case evidence.RoleSupporting:
			es.Supporting = append(es.Supporting, ev)
		}
	}

	for _, c := range conflicts {
		content, err := s.readUnitContent(c.sourceID, c.lineStart, c.lineEnd)
		if err != nil {
			slog.Warn("retrieval: build conflict evidence failed", "unit_id", c.unitID, "error", err)
			continue
		}

		ref := SourceRef{
			SourceID:  c.sourceID,
			LineStart: c.lineStart,
			LineEnd:   c.lineEnd,
		}
		refJSON, _ := json.Marshal(ref)

		title, _ := s.store.GetSourceTitle(c.sourceID)

		es.Conflicts = append(es.Conflicts, ConflictEvidence{
			UnitID:      c.unitID,
			PointID:     c.pointID,
			Content:     content,
			SourceRef:   refJSON,
			SourceTitle: title,
		})
	}

	return es, nil
}

// filterCurrentUnits drops candidates whose KU is no longer lifecycle=current.
func (s *Service) filterCurrentUnits(candidates []candidate) []candidate {
	kept := make([]candidate, 0, len(candidates))
	checked := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		current, ok := checked[c.unitID]
		if !ok {
			var err error
			current, err = s.store.UnitLifecycleCurrent(c.unitID)
			if err != nil {
				slog.Warn("retrieval: rerank lifecycle re-check failed, keeping candidate", "unit_id", c.unitID, "error", err)
				current = true
			}
			checked[c.unitID] = current
		}
		if current {
			kept = append(kept, c)
		} else {
			slog.Info("retrieval: rerank dropped candidate (KU no longer current)", "unit_id", c.unitID)
		}
	}
	return kept
}

func (s *Service) readUnitContent(sourceID string, lineStart, lineEnd int) (string, error) {
	mdPath, err := s.store.GetSourceMarkdownPath(sourceID)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(mdPath)
	if err != nil {
		return "", fmt.Errorf("retrieval: read markdown %s: %w", mdPath, err)
	}
	lines := strings.Split(string(data), "\n")
	return sliceLines(lines, lineStart, lineEnd), nil
}

func sliceLines(lines []string, lineStart, lineEnd int) string {
	if lineStart < 1 {
		lineStart = 1
	}
	if lineEnd > len(lines) {
		lineEnd = len(lines)
	}
	if lineStart > lineEnd {
		return ""
	}
	return strings.Join(lines[lineStart-1:lineEnd], "\n")
}

func intFromField(v interface{}) int {
	switch val := v.(type) {
	case float64:
		return int(val)
	case int:
		return val
	default:
		return 0
	}
}

// extractQuestionNouns extracts key entity phrases from the question.
// It splits on punctuation and particles, returning short phrases (2-8 chars)
// that are likely entity names (place, document, product, etc).
func extractQuestionNouns(question string) []string {
	// 多字疑问词分隔符必须排在单字分隔符（如"是"）之前，否则会被单字分隔符提前
	// 截断（如"是否"会被"是"拆成 ""+"否"，残留出"否..."这种无效片段）。
	splitters := []string{
		"如何", "是否", "能否", "怎么样", "怎么", "怎样", "为什么", "什么", "多少", "哪些",
		"，", "。", "？", "?", "！", "、", "：", "的", "中", "呢", "吗", "了", "是",
	}
	parts := []string{question}
	for _, sep := range splitters {
		var next []string
		for _, p := range parts {
			next = append(next, strings.Split(p, sep)...)
		}
		parts = next
	}

	// Also split each part by spaces
	var segments []string
	for _, p := range parts {
		for _, s := range strings.Fields(p) {
			s = strings.TrimSpace(s)
			r := []rune(s)
			if len(r) >= 2 && len(r) <= 8 {
				segments = append(segments, s)
			}
		}
	}

	// Filter out common verb/question words that are not entities
	commonWords := map[string]bool{
		"什么": true, "哪些": true, "怎么": true, "如何": true, "多少": true,
		"怎样": true, "为什么": true, "是否": true, "能否": true,
		"查询": true, "查看": true, "帮我": true, "看看": true, "分析": true,
		"标准": true, "怎么样": true, "是多少": true, "有哪些": true,
	}
	var nouns []string
	for _, s := range segments {
		if !commonWords[s] {
			nouns = append(nouns, s)
		}
	}
	return nouns
}

func containsAnyNoun(content string, nouns []string) bool {
	for _, n := range nouns {
		if strings.Contains(content, n) {
			return true
		}
	}
	return false
}

// lifecycleCurrentQuery conjoins base with a lifecycle=current TermQuery
// (docs/impl/v1/retrieval.md 步骤 5) so superseded/deprecated KU/KP never
// surface from the units/points Bleve indexes.
func lifecycleCurrentQuery(base query.Query) query.Query {
	lq := bleve.NewTermQuery("current")
	lq.SetField("lifecycle")
	return bleve.NewConjunctionQuery(base, lq)
}

// buildSourceIDQuery creates a Bleve boolean query to filter by source IDs.
func buildSourceIDQuery(sourceIDs []string) query.Query {
	if len(sourceIDs) == 0 {
		return nil
	}
	disjuncts := make([]query.Query, len(sourceIDs))
	for i, sid := range sourceIDs {
		tq := bleve.NewTermQuery(sid)
		tq.SetField("source_id")
		disjuncts[i] = tq
	}
	return bleve.NewDisjunctionQuery(disjuncts...)
}
