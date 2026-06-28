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
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

type Service struct {
	store         *Store
	llmClient     llm.LLMClient
	unitsIndex    bleve.Index
	pointsIndex   bleve.Index
	outlinesIndex bleve.Index
	cfg           *config.Config
}

func NewService(store *Store, llmClient llm.LLMClient, unitsIdx, pointsIdx, outlinesIdx bleve.Index, cfg *config.Config) *Service {
	return &Service{
		store:         store,
		llmClient:     llmClient,
		unitsIndex:    unitsIdx,
		pointsIndex:   pointsIdx,
		outlinesIndex: outlinesIdx,
		cfg:           cfg,
	}
}

func (s *Service) Retrieve(ctx context.Context, question string) (*EvidenceSet, error) {
	return s.RetrieveWithProgress(ctx, question, nil)
}

func (s *Service) RetrieveWithProgress(ctx context.Context, question string, progress ProgressFunc) (*EvidenceSet, error) {
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

	filteredSources, err := s.sourceSemanticFilter(ctx, question, candidateSources)
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
	outlineCandidates, err := s.outlineRecall(ctx, question, sourceIDs)
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
			Path:           "deep",
			DirectEvidence: []Evidence{},
			Supporting:     []Evidence{},
			Conflicts:      nil,
		}, nil
	}

	// Step 7: 证据分类（LLM Rerank）
	emit("rerank", "start", fmt.Sprintf("%d 条候选", len(merged)), 0)
	rerankStart := time.Now()
	reranked, err := s.rerank(ctx, question, merged)
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
	es, err := s.buildEvidenceSet(question, path, direct, supporting, conflictCandidates)
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

	model := s.cfg.LLM.ModelForPurpose("default").Model
	resp, err := s.llmClient.CompleteJSON(ctx, "question_domain_match.md", map[string]string{
		"question":    question,
		"domain_list": domainList.String(),
	}, model)
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

	return s.store.ListSourcesByDomainIDs(result.DomainIDs)
}

// Step 3: Source semantic filter
func (s *Service) sourceSemanticFilter(ctx context.Context, question string, candidates []SourceInfo) ([]SourceInfo, error) {
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

	model := s.cfg.LLM.ModelForPurpose("default").Model
	resp, err := s.llmClient.CompleteJSON(ctx, "source_filter.md", map[string]string{
		"question":    question,
		"source_list": sourceList.String(),
	}, model)
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
func (s *Service) outlineRecall(ctx context.Context, question string, sourceIDs []string) ([]candidate, error) {
	if len(sourceIDs) == 0 {
		return nil, nil
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

	q := bleve.NewMatchQuery(question)
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
		llmOutlineIDs, err := s.outlineLLMFallback(ctx, question, lowScoreSources)
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

func (s *Service) outlineLLMFallback(ctx context.Context, question string, sourceIDs []string) ([]string, error) {
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

			model := s.cfg.LLM.ModelForPurpose("default").Model
			resp, err := s.llmClient.CompleteJSON(ctx, "outline_filter.md", map[string]string{
				"question":     question,
				"outline_list": outlineList.String(),
			}, model)
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
	uq := bleve.NewMatchQuery(question)
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
	pq := bleve.NewMatchQuery(question)
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
func (s *Service) rerank(ctx context.Context, question string, candidates []candidate) ([]candidate, error) {
	// Assign candidate_ids
	for i := range candidates {
		candidates[i].candidateID = fmt.Sprintf("c%d", i+1)
	}

	// Build candidates text with content from markdown files
	var candidatesText strings.Builder
	for _, c := range candidates {
		content, err := s.readUnitContent(c.sourceID, c.lineStart, c.lineEnd)
		if err != nil {
			slog.Warn("retrieval: rerank read content failed", "unit_id", c.unitID, "error", err)
			continue
		}
		fmt.Fprintf(&candidatesText, "[%s] %s\n\n", c.candidateID, content)
	}

	jsonSchema := `{"results": [{"candidate_id": "c1", "role": "direct|supporting|irrelevant"}]}`

	resp, err := s.llmClient.CompleteJSON(ctx, "rerank.md", map[string]string{
		"question":    question,
		"candidates":  candidatesText.String(),
		"json_schema": jsonSchema,
	}, "classification")
	if err != nil {
		return nil, fmt.Errorf("retrieval: rerank llm: %w", err)
	}

	var rerankResult struct {
		Results []struct {
			CandidateID string `json:"candidate_id"`
			Role        string `json:"role"`
		} `json:"results"`
	}
	if err := json.Unmarshal(resp, &rerankResult); err != nil {
		return nil, fmt.Errorf("retrieval: rerank parse: %w", err)
	}

	// Validate
	cidSet := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		cidSet[c.candidateID] = true
	}
	for _, r := range rerankResult.Results {
		if !cidSet[r.CandidateID] {
			return nil, fmt.Errorf("retrieval: rerank returned unknown candidate_id: %s", r.CandidateID)
		}
		if r.Role != "direct" && r.Role != "supporting" && r.Role != "irrelevant" {
			return nil, fmt.Errorf("retrieval: rerank invalid role: %s", r.Role)
		}
	}

	// Map results back
	roleMap := make(map[string]string, len(rerankResult.Results))
	for _, r := range rerankResult.Results {
		roleMap[r.CandidateID] = r.Role
	}

	// Build content cache for direct validation
	contentCache := make(map[string]string, len(candidates))
	for _, c := range candidates {
		content, err := s.readUnitContent(c.sourceID, c.lineStart, c.lineEnd)
		if err == nil {
			contentCache[c.candidateID] = content
		}
	}

	// Extract key nouns from question for direct validation
	questionNouns := extractQuestionNouns(question)

	var kept []candidate
	for _, c := range candidates {
		role, ok := roleMap[c.candidateID]
		if !ok {
			continue
		}
		if role == "irrelevant" {
			continue
		}
		if role == "direct" && len(questionNouns) > 0 {
			content := contentCache[c.candidateID]
			if !containsAnyNoun(content, questionNouns) {
				slog.Info("retrieval: rerank direct→supporting (question noun not in evidence)",
					"candidate_id", c.candidateID, "nouns", questionNouns)
				role = "supporting"
			}
		}
		c.sourcePaths = []string{role}
		kept = append(kept, c)
	}
	return kept, nil
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
func (s *Service) buildEvidenceSet(question, path string, direct, supporting, conflicts []candidate) (*EvidenceSet, error) {
	es := &EvidenceSet{
		Question:       question,
		Path:           path,
		DirectEvidence: make([]Evidence, 0, len(direct)),
		Supporting:     make([]Evidence, 0, len(supporting)),
	}

	buildEvidence := func(c candidate, role string) (Evidence, error) {
		content, err := s.readUnitContent(c.sourceID, c.lineStart, c.lineEnd)
		if err != nil {
			return Evidence{}, err
		}

		ref := SourceRef{
			SourceID:  c.sourceID,
			LineStart: c.lineStart,
			LineEnd:   c.lineEnd,
		}
		refJSON, _ := json.Marshal(ref)

		return Evidence{
			FactID:    uuid.New().String(),
			UnitID:    c.unitID,
			PointID:   c.pointID,
			Content:   content,
			SourceRef: refJSON,
			Role:      role,
		}, nil
	}

	for _, c := range direct {
		ev, err := buildEvidence(c, "direct")
		if err != nil {
			slog.Warn("retrieval: build evidence failed", "unit_id", c.unitID, "error", err)
			continue
		}
		es.DirectEvidence = append(es.DirectEvidence, ev)
	}

	for _, c := range supporting {
		ev, err := buildEvidence(c, "supporting")
		if err != nil {
			slog.Warn("retrieval: build evidence failed", "unit_id", c.unitID, "error", err)
			continue
		}
		es.Supporting = append(es.Supporting, ev)
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
	splitters := []string{"，", "。", "？", "?", "！", "、", "：", "的", "中", "呢", "吗", "了", "是"}
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
