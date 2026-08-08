// Topic-page candidate detection and second-tier compilation
// (docs/impl/v1/wiki.md 步骤 8; 2026-08-03 修订: quadruple clustering over
// real questions replaces connected-component clustering over published
// concept pages — see the doc's "主题：从真实使用中识别，而不是从已发布词条
// 事后聚类").
package wiki

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/search/query"
	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/activation"
)

var requiredTopicSections = []string{"## 主题概览", "## 主线结论", "## 子主题分工", "## 跨主题矛盾与待验证点", "## 依赖页面"}

// TopicCandidate is one quadruple-cluster group that cleared 二阶编译准入
// (docs/impl/v1/wiki.md 步骤 8, 2026-08-03 修订): a draft topic shell page
// was created with contains rows for every already-published member.
type TopicCandidate struct {
	PageID           string   // the newly created draft shell page
	MemberPageIDs    []string // published members with a contains row
	PendingEntries   []string // entry_id list that cleared 步骤3 ready but has no published page yet — "待发布成员", a wiki_candidate was written and will get a contains row once it publishes (步骤 7b)
	UncoveredEntries []string // entry_id list that did NOT clear 步骤3 ready — 缺材料, no wiki_candidate written
	RelatedCount     int
	ContradictsCount int
	Reason           string
}

// TopicSignalUnderfilled is 步骤 8 第 4 步's "有需求、缺材料" outcome: a
// quadruple cluster cleared the stable-cluster gate but the candidate-range
// KP retrieval produced zero qualifying KP, so no shell page was created.
// Report-only (study.md 步骤 6 "topic_signal_underfilled").
type TopicSignalUnderfilled struct {
	Subject               string
	Intent                string
	Audience              string
	ConstraintText        string
	DistinctQuestionCount int
	DaysActive            int
}

// lifecyclePointsQuery mirrors retrieval.lifecycleCurrentQuery for the
// points index (unexported there, so re-derived here rather than exported
// cross-package just for this one call site).
func lifecyclePointsQuery(text string) query.Query {
	mq := bleve.NewMatchQuery(text)
	lq := bleve.NewTermQuery("current")
	lq.SetField("lifecycle")
	return bleve.NewConjunctionQuery(mq, lq)
}

// DetectTopicCandidate implements docs/impl/v1/wiki.md 步骤 8 第 3-10 步 for
// one already-qualified quadruple cluster (稳定簇判定, done by the caller —
// Study — before calling in; see study/service.go's flagTopicPageCandidates).
// queryText is the cluster's subject/intent/audience/constraint_text
// concatenation; kpMin is study.wiki_kp_min, the "广度" leg of 步骤 3's
// concept-level ready judgment, passed in rather than duplicated into
// wiki.Config since Study owns that threshold.
//
// Returns (candidate, nil, nil) when a shell page was created;
// (nil, underfilled, nil) when the candidate range had zero topic-scope
// material KPs (步骤 4: lifecycle=current; verified not required);
// (nil, nil, nil) when the cluster failed 二阶准入 (步骤 7:
// insufficient related-connection dominance, insufficient reliability, or
// fewer than 2 published members) — logged, not persisted as a report item,
// since 步骤 7 doesn't name a learning_result object for the no-shell case
// (a judgment call: the doc's "写入 learning_result.reason" reads most
// naturally as documenting the *reason field of a result the caller may or
// may not choose to keep* — but with no page_id yet minted there is nothing
// canonical to hang a pending_confirm result on, so we surface it only via
// structured logging here and let the caller decide whether to elevate it).
func (s *Service) DetectTopicCandidate(subject, intent, audience, constraintText string, distinctQuestionCount, daysActive, kpMin int) (*TopicCandidate, *TopicSignalUnderfilled, error) {
	queryText := strings.TrimSpace(subject + " " + intent + " " + audience + " " + constraintText)
	// 步骤 3: candidate-range KP retrieval.
	kpMax := s.cfg.TopicCandidateKPMax
	if kpMax <= 0 {
		kpMax = 50
	}
	req := bleve.NewSearchRequest(lifecyclePointsQuery(queryText))
	req.Size = kpMax
	req.Fields = []string{"point_id"}
	var candidateIDs []string
	if s.pointsIndex != nil {
		res, err := s.pointsIndex.Search(req)
		if err != nil {
			return nil, nil, fmt.Errorf("wiki: topic candidate range search: %w", err)
		}
		for _, hit := range res.Hits {
			candidateIDs = append(candidateIDs, hit.ID)
		}
	}
	if len(candidateIDs) > kpMax {
		candidateIDs = candidateIDs[:kpMax]
	}

	// 步骤 4: topic-scope material filter (lifecycle=current only;
	// verified is NOT required — see QualifyingPointsByIDs).
	qualifying, err := s.store.QualifyingPointsByIDs(candidateIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("wiki: topic candidate qualifying filter: %w", err)
	}
	if len(qualifying) == 0 {
		return nil, &TopicSignalUnderfilled{
			Subject: subject, Intent: intent, Audience: audience, ConstraintText: constraintText,
			DistinctQuestionCount: distinctQuestionCount, DaysActive: daysActive,
		}, nil
	}

	// 步骤 5: group by entry_id.
	byEntry := make(map[string][]string)
	var conceptOrder []string
	for _, q := range qualifying {
		if _, ok := byEntry[q.EntryID]; !ok {
			conceptOrder = append(conceptOrder, q.EntryID)
		}
		byEntry[q.EntryID] = append(byEntry[q.EntryID], q.PointID)
	}
	sort.Strings(conceptOrder)

	// 步骤 6: per-concept resolution.
	var publishedMembers []string
	var pendingEntries []string
	var uncoveredConcepts []string
	for _, conceptID := range conceptOrder {
		page, err := s.store.GetActivePageByEntryID(conceptID)
		if err != nil {
			slog.Warn("wiki: topic candidate concept page lookup failed", "entry_id", conceptID, "error", err)
			uncoveredConcepts = append(uncoveredConcepts, conceptID)
			continue
		}
		if page != nil && page.Status == StatusPublished {
			publishedMembers = append(publishedMembers, page.PageID)
			continue
		}
		ready, _, err := s.isEntryReady(conceptID, kpMin)
		if err != nil {
			slog.Warn("wiki: topic candidate concept readiness check failed", "entry_id", conceptID, "error", err)
			uncoveredConcepts = append(uncoveredConcepts, conceptID)
			continue
		}
		if !ready {
			uncoveredConcepts = append(uncoveredConcepts, conceptID)
			continue
		}
		if s.activationSvc == nil {
			// Can't record the wiki_candidate — falls back to uncovered rather
			// than silently claiming "pending" with nothing behind it.
			uncoveredConcepts = append(uncoveredConcepts, conceptID)
			continue
		}
		pending, err := s.store.HasPendingWikiCandidate(conceptID)
		if err != nil {
			slog.Warn("wiki: topic candidate check pending wiki_candidate failed", "entry_id", conceptID, "error", err)
		}
		if !pending {
			lr := &activation.LearningResult{
				Action:     activation.ActionWikiCandidate,
				ObjectType: activation.ObjectTypeWikiPage,
				ObjectID:   conceptID,
				Reason:     "主题候选随批推进：该概念的 qualifying KP 满足概念级 ready 判定",
				EventIDs:   marshalIDs(byEntry[conceptID]),
				Status:     activation.ResultPendingConfirm,
			}
			if err := s.activationSvc.Store().InsertLearningResult(lr); err != nil {
				slog.Warn("wiki: topic candidate insert member wiki_candidate failed", "entry_id", conceptID, "error", err)
			}
		}
		// Not published yet — contains row deferred to 步骤 7b (recomputePageRelations)
		// once this concept's own compile publishes it. "待发布成员": ready and
		// (now or already) has a pending wiki_candidate, distinct from
		// uncovered_entries (缺材料, not ready).
		pendingEntries = append(pendingEntries, conceptID)
	}

	// 成员数 < 2 时无意义，直接跳过（步骤 7）。
	if len(publishedMembers) < 2 {
		slog.Info("wiki: topic candidate skipped, fewer than 2 published members",
			"published_members", len(publishedMembers), "distinct_question_count", distinctQuestionCount, "days_active", daysActive)
		return nil, nil, nil
	}
	sort.Strings(publishedMembers)

	// 步骤 7: 二阶编译准入 — 关联 + 整体可靠度.
	relatedCount, err := s.store.CountRelationEdgesWithin(publishedMembers, RelationRelated)
	if err != nil {
		return nil, nil, fmt.Errorf("wiki: topic candidate count related edges: %w", err)
	}
	contradictsCount, err := s.store.CountRelationEdgesWithin(publishedMembers, RelationContradicts)
	if err != nil {
		return nil, nil, fmt.Errorf("wiki: topic candidate count contradicts edges: %w", err)
	}
	relatedOK := relatedCount >= 1 && relatedCount >= contradictsCount

	reliabilityMin := s.cfg.TopicReliabilityMin
	if reliabilityMin <= 0 {
		reliabilityMin = 0.5
	}
	reliability, err := s.store.VerifiedFraction(candidateIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("wiki: topic candidate reliability: %w", err)
	}
	reliabilityOK := reliability >= reliabilityMin

	if !relatedOK || !reliabilityOK {
		reason := "关联不够"
		if relatedOK {
			reason = "整体可靠度不够"
		}
		slog.Info("wiki: topic candidate failed second-tier admission", "reason", reason,
			"related", relatedCount, "contradicts", contradictsCount, "reliability", reliability, "reliability_min", reliabilityMin)
		return nil, nil, nil
	}

	// 步骤 8: create draft shell + contains rows for published members.
	cand, err := s.createTopicShell(publishedMembers, relatedCount, contradictsCount, "[]")
	if err != nil {
		return nil, nil, fmt.Errorf("wiki: create topic shell failed: %w", err)
	}
	cand.PendingEntries = pendingEntries
	cand.UncoveredEntries = uncoveredConcepts
	cand.Reason = fmt.Sprintf("四元组聚类：不同问法 %d 个，活跃天数 %d 天；已发布成员 %d 个，related 边 %d 条，contradicts 边 %d 条，整体可靠度 %.2f；待发布成员 %d 个，未覆盖概念 %d 个",
		distinctQuestionCount, daysActive, len(publishedMembers), relatedCount, contradictsCount, reliability, len(pendingEntries), len(uncoveredConcepts))
	return cand, nil, nil
}

// isEntryReady applies docs/impl/v1/wiki.md 步骤 3's "概念级 ready 判定"
// four gates (广度/连贯/稳定/内聚), reusing gatherAnalyzeInputs +
// computeReadiness — the exact same material-gathering and stats Analyze
// already computes for a human-triggered compile, so this isn't a second
// implementation of the qualifying-KP/cohesion SQL. kpMin ("广度" leg) is a
// parameter because it's study.wiki_kp_min, owned by Study's config.
func (s *Service) isEntryReady(conceptID string, kpMin int) (bool, *Readiness, error) {
	// Readiness stays verified-gated regardless of trigger source (docs/impl/
	// v1/wiki.md 步骤 2 2026-08-07 修订: only the qualifying-for-compile
	// definition drops verified on the manual path — "ready" is a distinct,
	// unchanged signal).
	in, err := s.gatherAnalyzeInputs(conceptID, true)
	if err != nil {
		// No qualifying points (or lookup failure) — not ready, not an error
		// the caller needs to abort over.
		return false, nil, nil
	}
	r := s.computeReadiness(in)
	if kpMin <= 0 {
		kpMin = 4
	}
	breadthOK := r.QualifyingKPCount >= kpMin
	relatedOK := r.RelatedConnectionCount >= 1 && r.ContradictsConnectionCount < r.RelatedConnectionCount
	stableOK := r.DaysActive >= s.cfg.QualifyingMinDaysActive
	cohesionOK := s.cfg.EntryCohesionMin <= 0 || r.Cohesion >= s.cfg.EntryCohesionMin
	return breadthOK && relatedOK && stableOK && cohesionOK, r, nil
}

// TopicReadiness is the informational snapshot returned by CreateTopicManual
// (docs/impl/v1/wiki.md 步骤 8 "人工手动指定主题") — Study's admission
// signals are shown, not used as a gate for the manual-trigger path.
type TopicReadiness struct {
	MemberCount                int     `json:"member_count"`
	RelatedConnectionCount     int     `json:"related_connection_count"`
	ContradictsConnectionCount int     `json:"contradicts_connection_count"`
	ReliabilityRatio           float64 `json:"reliability_ratio"`
	ReliabilityMin             float64 `json:"reliability_min"`
}

// ErrInvalidTopicMembers is returned by CreateTopicManual when the caller
// supplied an unusable topic_name/domain_id. Handler maps it to HTTP 400.
type ErrInvalidTopicMembers struct {
	Message string
}

func (e *ErrInvalidTopicMembers) Error() string { return e.Message }

// retrieveAndGroupQualifyingKPs is 步骤 8 第 1-1b 步 shared between
// CreateTopicManual (一把梭) and PreviewTopicCandidates (分步向导第 1 步,
// 2026-08-07 新增): candidate-range KP retrieval — full-text ∪ outline
// recall (2026-08-07 再次修订, 对齐检索慢路径的召回质量; 只覆盖这两条人工
// 触发口径, 不含 Study 自动路径 DetectTopicCandidate, 见 docs/impl/v1/
// wiki.md 步骤 8「分步向导」末尾的范围说明) — 之后 domain 过滤 + qualifying
// 过滤 + LLM 相关性判定 + group by entry_id。
func (s *Service) retrieveAndGroupQualifyingKPs(ctx context.Context, topicName, topicDescription, domainID string) (byEntry map[string][]string, conceptOrder []string, candidateIDs []string, err error) {
	if strings.TrimSpace(topicName) == "" {
		return nil, nil, nil, &ErrInvalidTopicMembers{Message: "wiki: topic_name is required"}
	}
	queryText := strings.TrimSpace(topicName + " " + topicDescription)

	kpMax := s.cfg.TopicCandidateKPMax
	if kpMax <= 0 {
		kpMax = 50
	}

	seen := make(map[string]bool)
	addCandidate := func(pointID string) {
		if !seen[pointID] {
			seen[pointID] = true
			candidateIDs = append(candidateIDs, pointID)
		}
	}

	// 1a. 全文检索（points 索引）。
	req := bleve.NewSearchRequest(lifecyclePointsQuery(queryText))
	req.Size = kpMax
	req.Fields = []string{"point_id"}
	if s.pointsIndex != nil {
		res, err := s.pointsIndex.Search(req)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("wiki: manual topic candidate search: %w", err)
		}
		for _, hit := range res.Hits {
			addCandidate(hit.ID)
		}
	}

	// 1b. 目录结构检索（outlines 索引）：命中的目录节点按来源分组、展开
	// 子孙节点，解析出知识单元，取单元下全部 current KP（要广度，不是
	// 慢路径 outlineRecall 那种"每单元取一条代表 KP"）。
	if outlineCandidates, err := s.outlineRecallCandidates(queryText); err != nil {
		slog.Warn("wiki: outline recall failed, continuing with full-text candidates only", "error", err)
	} else {
		for _, pid := range outlineCandidates {
			addCandidate(pid)
		}
	}

	// domain 过滤统一在并集上做一次（此前是内联在全文检索循环里，目录检索
	// 分支加入后必须挪到并集之后，否则目录来源的候选漏过滤）。
	if domainID != "" {
		filtered := candidateIDs[:0]
		for _, pid := range candidateIDs {
			d, err := s.store.PointDomainID(pid)
			if err == nil && d != domainID {
				continue
			}
			filtered = append(filtered, pid)
		}
		candidateIDs = filtered
	}
	if len(candidateIDs) > kpMax {
		candidateIDs = candidateIDs[:kpMax]
	}

	qualifying, err := s.store.QualifyingPointsByIDs(candidateIDs)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("wiki: manual topic qualifying filter: %w", err)
	}
	if len(qualifying) == 0 {
		return nil, nil, nil, &ErrInvalidTopicMembers{Message: "wiki: no current knowledge points found for this topic scope"}
	}

	qualifying, err = s.judgeTopicCandidateRelevance(ctx, topicName, topicDescription, qualifying)
	if err != nil {
		slog.Warn("wiki: topic candidate relevance judge failed, keeping all qualifying candidates", "error", err)
	}
	if len(qualifying) == 0 {
		return nil, nil, nil, &ErrInvalidTopicMembers{Message: "wiki: no current knowledge points found for this topic scope"}
	}

	byEntry = make(map[string][]string)
	for _, q := range qualifying {
		if _, ok := byEntry[q.EntryID]; !ok {
			conceptOrder = append(conceptOrder, q.EntryID)
		}
		byEntry[q.EntryID] = append(byEntry[q.EntryID], q.PointID)
	}
	sort.Strings(conceptOrder)
	return byEntry, conceptOrder, candidateIDs, nil
}

// outlineRecallCandidates implements 步骤 8 候选检索 1b：目录结构召回，
// mirrors 检索慢路径 outlineRecall 的召回结构 (bleve match → 按来源分组
// → 展开子孙节点 → 解析知识单元) 但简化 (不设分数门槛、不做 LLM 兜底、
// 每单元取全部 current KP 而非一条代表 KP)。
func (s *Service) outlineRecallCandidates(queryText string) ([]string, error) {
	if s.outlinesIndex == nil {
		return nil, nil
	}
	req := bleve.NewSearchRequest(bleve.NewMatchQuery(queryText))
	req.Size = 100
	req.Fields = []string{"source_id"}
	res, err := s.outlinesIndex.Search(req)
	if err != nil {
		return nil, fmt.Errorf("wiki: outline candidate search: %w", err)
	}

	bySource := make(map[string][]string)
	for _, hit := range res.Hits {
		sourceID, _ := hit.Fields["source_id"].(string)
		if sourceID == "" {
			continue
		}
		bySource[sourceID] = append(bySource[sourceID], hit.ID)
	}

	var unitIDs []string
	for sourceID, outlineIDs := range bySource {
		expanded, err := s.store.ChildOutlineIDs(outlineIDs, sourceID)
		if err != nil {
			slog.Warn("wiki: outline child expansion failed, using direct hits only", "source_id", sourceID, "error", err)
			expanded = outlineIDs
		}
		ids, err := s.store.UnitIDsByOutlineIDs(expanded)
		if err != nil {
			return nil, fmt.Errorf("wiki: unit ids by outline ids: %w", err)
		}
		unitIDs = append(unitIDs, ids...)
	}
	if len(unitIDs) == 0 {
		return nil, nil
	}
	return s.store.PointIDsByUnitIDs(unitIDs)
}

// judgeTopicCandidateRelevance implements 步骤 8 候选检索 1b 的 LLM 相关性
// 判定：候选检索是召回，混有仅词面相关、实际不属于该主题范围的材料——
// 批量让模型判断每条候选是否真的属于这个主题范围。LLM 调用或解析失败时
// fail-open（该批候选原样保留），不因为判定环节本身出错反而让候选变少。
func (s *Service) judgeTopicCandidateRelevance(ctx context.Context, topicName, topicDescription string, qualifying []QualifyingPointRef) ([]QualifyingPointRef, error) {
	if len(qualifying) == 0 {
		return qualifying, nil
	}
	pointIDs := make([]string, len(qualifying))
	for i, q := range qualifying {
		pointIDs[i] = q.PointID
	}
	semantics, err := s.store.CandidateSemantics(pointIDs)
	if err != nil {
		return qualifying, fmt.Errorf("wiki: candidate semantics: %w", err)
	}
	if len(semantics) == 0 {
		return qualifying, nil
	}

	batchMaxChars := s.cfg.TopicRerankBatchMaxChars
	if batchMaxChars <= 0 {
		batchMaxChars = 6000
	}

	type judgeCandidate struct {
		CandidateID  string `json:"candidate_id"`
		Content      string `json:"content"`
		UnitCenter   string `json:"unit_center"`
		SourceTitle  string `json:"source_title"`
		SourceTheme  string `json:"source_theme"`
		ContentTheme string `json:"content_theme"`
		Intent       string `json:"intent"`
		Object       string `json:"object"`
		Scope        string `json:"scope"`
	}

	relevant := make(map[string]bool, len(semantics))
	var batch []judgeCandidate
	batchChars := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		candidatesJSON, err := json.Marshal(batch)
		if err != nil {
			return fmt.Errorf("wiki: marshal topic candidates: %w", err)
		}
		raw, err := s.llmClient.CompleteJSON(ctx, "wiki_topic_candidate_rerank.md", map[string]string{
			"topic_name":        topicName,
			"topic_description": topicDescription,
			"candidates":        string(candidatesJSON),
		}, "classification")
		if err != nil {
			for _, c := range batch {
				relevant[c.CandidateID] = true // fail-open
			}
			return fmt.Errorf("wiki: topic candidate rerank llm call: %w", err)
		}
		var output struct {
			Results []struct {
				CandidateID string `json:"candidate_id"`
				Relevant    bool   `json:"relevant"`
				Reason      string `json:"reason"`
			} `json:"results"`
		}
		if err := json.Unmarshal(raw, &output); err != nil {
			for _, c := range batch {
				relevant[c.CandidateID] = true // fail-open
			}
			return fmt.Errorf("wiki: topic candidate rerank parse: %w", err)
		}
		for _, r := range output.Results {
			relevant[r.CandidateID] = r.Relevant
		}
		// A candidate the model dropped from its response is kept, not
		// dropped (fail-open, same rationale as an outright call failure).
		for _, c := range batch {
			if _, ok := relevant[c.CandidateID]; !ok {
				relevant[c.CandidateID] = true
			}
		}
		return nil
	}

	var flushErr error
	for _, sem := range semantics {
		c := judgeCandidate{
			CandidateID: sem.PointID, Content: sem.Content, UnitCenter: sem.UnitCenter,
			SourceTitle: sem.SourceTitle, SourceTheme: sem.SourceTheme, ContentTheme: sem.ContentTheme,
			Intent: sem.Intent, Object: sem.Object, Scope: sem.Scope,
		}
		cChars := len(c.Content) + len(c.UnitCenter) + len(c.SourceTitle) + len(c.SourceTheme) + len(c.ContentTheme) + len(c.Intent) + len(c.Object) + len(c.Scope)
		if len(batch) > 0 && batchChars+cChars > batchMaxChars {
			if err := flush(); err != nil {
				flushErr = err
			}
			batch = nil
			batchChars = 0
		}
		batch = append(batch, c)
		batchChars += cChars
	}
	if err := flush(); err != nil {
		flushErr = err
	}

	out := make([]QualifyingPointRef, 0, len(qualifying))
	for _, q := range qualifying {
		if relevant[q.PointID] {
			out = append(out, q)
		}
	}
	return out, flushErr
}

// TopicCandidateEntry is one qualifying entry in a PreviewTopicCandidates
// response (docs/impl/v1/wiki.md 步骤 8 "分步向导" 步骤 1) — enough for a
// human to decide whether to compile it via the existing POST /wiki/compile.
type TopicCandidateEntry struct {
	EntryID                string     `json:"entry_id"`
	EntryName              string     `json:"entry_name"`
	QualifyingKPCount      int        `json:"qualifying_kp_count"`
	AlreadyPublishedPageID string     `json:"already_published_page_id,omitempty"`
	DraftPageID            string     `json:"draft_page_id,omitempty"`
	IsReady                bool       `json:"is_ready"`
	Readiness              *Readiness `json:"readiness,omitempty"`
}

// PreviewTopicCandidates implements 步骤 8 "分步向导" 步骤 1: the same
// retrieval + qualifying + grouping as CreateTopicManual, but read-only — no
// wiki_candidate learning_result written, no shell page created. Lets a human
// see, per entry, whether it's already published, whether it clears the
// isEntryReady gate, and how many qualifying KP it has, before deciding
// whether to force-compile it via POST /wiki/compile (which has no ready
// gate of its own).
func (s *Service) PreviewTopicCandidates(ctx context.Context, topicName, topicDescription, domainID string) ([]TopicCandidateEntry, error) {
	byEntry, conceptOrder, _, err := s.retrieveAndGroupQualifyingKPs(ctx, topicName, topicDescription, domainID)
	if err != nil {
		return nil, err
	}

	out := make([]TopicCandidateEntry, 0, len(conceptOrder))
	for _, entryID := range conceptOrder {
		name, _, _, _, err := s.store.GetEntryInfo(entryID)
		if err != nil {
			slog.Warn("wiki: preview topic candidate entry info lookup failed", "entry_id", entryID, "error", err)
		}
		entry := TopicCandidateEntry{
			EntryID:           entryID,
			EntryName:         name,
			QualifyingKPCount: len(byEntry[entryID]),
		}
		s.entryPublishReadiness(&entry)
		out = append(out, entry)
	}
	return out, nil
}

// entryPublishReadiness fills entry.AlreadyPublishedPageID/IsReady/Readiness
// in place — the per-entry "is this already published, or does it clear the
// informational ready signal" lookup shared by PreviewTopicCandidates and
// GetWizardTaskDetail's live refresh (docs/impl/v1/wiki.md 步骤 8 "分步向导"
// 断点续开, 2026-08-07): a task reopened after the human compiled/published
// entries in an earlier session must show current state, not the snapshot
// from when the (expensive) retrieval first ran.
func (s *Service) entryPublishReadiness(entry *TopicCandidateEntry) {
	const wikiKPMin = 0 // same fallback-to-4 rationale as CreateTopicManual
	entry.AlreadyPublishedPageID = ""
	entry.DraftPageID = ""
	if page, err := s.store.GetActivePageByEntryID(entry.EntryID); err == nil && page != nil {
		if page.Status == StatusPublished {
			entry.AlreadyPublishedPageID = page.PageID
			entry.IsReady = true
			entry.Readiness = nil
			return
		}
		// Compiled but not published yet (draft/needs_recompile) — surfaced
		// so a reopened wizard task shows "编译过、待发布" instead of
		// re-offering a 编译 button that would 409 (ErrPageAlreadyExists).
		entry.DraftPageID = page.PageID
	}
	ready, r, err := s.isEntryReady(entry.EntryID, wikiKPMin)
	if err != nil {
		slog.Warn("wiki: entry publish readiness check failed", "entry_id", entry.EntryID, "error", err)
	}
	entry.IsReady = ready
	entry.Readiness = r
}

// WizardTaskDetail is GET /wiki/wizard/tasks/:id's response shape
// (docs/impl/v1/wiki.md 步骤 8 "分步向导" 断点续开, 2026-08-07 新增).
type WizardTaskDetail struct {
	TaskID           string                `json:"task_id"`
	DomainID         string                `json:"domain_id"`
	TopicName        string                `json:"topic_name"`
	TopicDescription string                `json:"topic_description"`
	Status           string                `json:"status"`
	Entries          []TopicCandidateEntry `json:"entries"`
	SelectedMembers  []string              `json:"selected_members"`
	ErrorMessage     string                `json:"error_message,omitempty"`
}

// StartWizardTask implements 步骤 8 "分步向导" 步骤 1 的断点续开入口: if
// domainID already has an active task (UNIQUE constraint, at most one),
// return it as-is rather than starting a second retrieval — the caller
// resumes from whatever status it's in. Otherwise inserts a
// candidates_loading row and launches the (expensive, 30-60s) retrieval in
// a background goroutine using context.Background() (NOT the caller's
// request ctx, which is canceled the moment the HTTP handler returns) so the
// LLM calls survive past the request that started them.
func (s *Service) StartWizardTask(topicName, topicDescription, domainID string) (*WizardTask, error) {
	if strings.TrimSpace(topicName) == "" {
		return nil, &ErrInvalidTopicMembers{Message: "wiki: topic_name is required"}
	}
	if strings.TrimSpace(domainID) == "" {
		return nil, &ErrInvalidTopicMembers{Message: "wiki: domain_id is required for a wizard task"}
	}

	existing, err := s.store.GetWizardTaskByDomain(domainID)
	if err != nil {
		return nil, fmt.Errorf("wiki: get wizard task by domain: %w", err)
	}
	if existing != nil {
		return existing, nil
	}

	task := &WizardTask{
		TaskID:           uuid.New().String(),
		DomainID:         domainID,
		TopicName:        topicName,
		TopicDescription: topicDescription,
		Status:           WizardTaskStatusCandidatesLoading,
	}
	if err := s.store.InsertWizardTask(task); err != nil {
		return nil, err
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		entries, err := s.PreviewTopicCandidates(ctx, topicName, topicDescription, domainID)
		if err != nil {
			slog.Warn("wiki: wizard task candidate retrieval failed", "task_id", task.TaskID, "error", err)
			if uerr := s.store.UpdateWizardTaskError(task.TaskID, err.Error()); uerr != nil {
				slog.Error("wiki: update wizard task error failed", "task_id", task.TaskID, "error", uerr)
			}
			return
		}
		candidatesJSON, err := json.Marshal(entries)
		if err != nil {
			slog.Error("wiki: marshal wizard task candidates failed", "task_id", task.TaskID, "error", err)
			_ = s.store.UpdateWizardTaskError(task.TaskID, "internal error: marshal candidates")
			return
		}
		if err := s.store.UpdateWizardTaskCandidatesReady(task.TaskID, string(candidatesJSON)); err != nil {
			slog.Error("wiki: update wizard task candidates ready failed", "task_id", task.TaskID, "error", err)
		}
	}()

	return task, nil
}

// GetWizardTaskDetail loads a task and, when candidates_ready, live-refreshes
// each cached entry's AlreadyPublishedPageID/IsReady (entryPublishReadiness)
// so reopening a task after compiling/publishing entries in an earlier
// session shows current state instead of the retrieval-time snapshot.
// qualifying_kp_count/entry_name stay cached — refreshing them would mean
// re-running the expensive retrieval, which defeats the point of persisting
// the task in the first place.
func (s *Service) GetWizardTaskDetail(taskID string) (*WizardTaskDetail, error) {
	task, err := s.store.GetWizardTaskByID(taskID)
	if err != nil {
		return nil, fmt.Errorf("wiki: get wizard task: %w", err)
	}
	if task == nil {
		return nil, nil
	}

	var entries []TopicCandidateEntry
	if err := json.Unmarshal([]byte(task.CandidatesJSON), &entries); err != nil {
		slog.Warn("wiki: unmarshal wizard task candidates failed", "task_id", taskID, "error", err)
	}
	if task.Status == WizardTaskStatusCandidatesReady {
		for i := range entries {
			s.entryPublishReadiness(&entries[i])
		}
	}

	var selected []string
	if err := json.Unmarshal([]byte(task.SelectedMembersJSON), &selected); err != nil {
		slog.Warn("wiki: unmarshal wizard task selected members failed", "task_id", taskID, "error", err)
	}

	return &WizardTaskDetail{
		TaskID: task.TaskID, DomainID: task.DomainID, TopicName: task.TopicName,
		TopicDescription: task.TopicDescription, Status: task.Status,
		Entries: entries, SelectedMembers: selected, ErrorMessage: task.ErrorMessage,
	}, nil
}

func (s *Service) UpdateWizardTaskSelectedMembers(taskID string, memberPageIDs []string) error {
	b, err := json.Marshal(memberPageIDs)
	if err != nil {
		return fmt.Errorf("wiki: marshal selected members: %w", err)
	}
	return s.store.UpdateWizardTaskSelectedMembers(taskID, string(b))
}

func (s *Service) DeleteWizardTask(taskID string) error {
	return s.store.DeleteWizardTask(taskID)
}

// CreateTopicFromMembers implements 步骤 8 "分步向导" 步骤 3: build a draft
// topic shell from an explicit, human-picked list of already-published
// concept/fact pages — unlike CreateTopicManual, membership is not computed
// from isEntryReady, it's given directly. Rejects topic-type pages (no
// nesting — 两层架构 only) and any page not yet published.
func (s *Service) CreateTopicFromMembers(topicName string, memberPageIDs []string) (*TopicCandidate, error) {
	if strings.TrimSpace(topicName) == "" {
		return nil, &ErrInvalidTopicMembers{Message: "wiki: topic_name is required"}
	}
	if len(memberPageIDs) == 0 {
		return nil, &ErrInvalidTopicMembers{Message: "wiki: member_page_ids is required"}
	}

	pending := make(map[string]string)
	for _, id := range memberPageIDs {
		p, err := s.store.GetPage(id)
		if err != nil {
			return nil, fmt.Errorf("wiki: get member page: %w", err)
		}
		if p == nil {
			pending[id] = "not_found"
			continue
		}
		if p.PageType == PageTypeTopic {
			pending[id] = "topic_page_not_allowed_as_member"
			continue
		}
		if p.Status != StatusPublished {
			pending[id] = p.Status
		}
	}
	if len(pending) > 0 {
		return nil, &ErrMembersNotPublished{Pending: pending}
	}

	relatedCount, err := s.store.CountRelationEdgesWithin(memberPageIDs, RelationRelated)
	if err != nil {
		return nil, fmt.Errorf("wiki: draft topic count related edges: %w", err)
	}
	contradictsCount, err := s.store.CountRelationEdgesWithin(memberPageIDs, RelationContradicts)
	if err != nil {
		return nil, fmt.Errorf("wiki: draft topic count contradicts edges: %w", err)
	}

	cand, err := s.createTopicShell(memberPageIDs, relatedCount, contradictsCount, marshalIDs([]string{ManualTriggerSentinel}))
	if err != nil {
		return nil, err
	}
	cand.Reason = fmt.Sprintf("分步向导人工显式指定主题 %q 成员 %d 个，related 边 %d 条，contradicts 边 %d 条",
		topicName, len(memberPageIDs), relatedCount, contradictsCount)

	slog.Info("wiki: created topic shell from explicit members", "page_id", cand.PageID, "members", len(memberPageIDs))
	return cand, nil
}

// CreateTopicManual implements docs/impl/v1/wiki.md 步骤 8 "人工手动指定
// 主题" (2026-08-03 修订): the human gives a topic *scope* (name +
// description [+ domain]), not a member-page list — the same candidate-range
// retrieval / qualifying filter / concept grouping as Study's automatic path
// (步骤 8 第 3-6 步), just triggered manually and with admission (步骤 7)
// shown rather than enforced. No topic_page_candidate learning_result is
// written (no pending_confirm object to resolve — the shell itself can be
// archived directly to reject it).
func (s *Service) CreateTopicManual(ctx context.Context, topicName, topicDescription, domainID string) (*TopicCandidate, *TopicReadiness, error) {
	byEntry, conceptOrder, candidateIDs, err := s.retrieveAndGroupQualifyingKPs(ctx, topicName, topicDescription, domainID)
	if err != nil {
		return nil, nil, err
	}

	// wiki.Service has no visibility into study.wiki_kp_min (owned by
	// Study's config); isEntryReady falls back to its own default (4) when
	// passed 0, which is an acceptable approximation for this manual,
	// informational-only path (readiness here never gates anything — see
	// doc comment above).
	const wikiKPMin = 0

	var publishedMembers, pendingEntries, uncoveredConcepts []string
	for _, conceptID := range conceptOrder {
		page, err := s.store.GetActivePageByEntryID(conceptID)
		if err != nil {
			uncoveredConcepts = append(uncoveredConcepts, conceptID)
			continue
		}
		if page != nil && page.Status == StatusPublished {
			publishedMembers = append(publishedMembers, page.PageID)
			continue
		}
		ready, _, err := s.isEntryReady(conceptID, wikiKPMin)
		if err == nil && ready && s.activationSvc != nil {
			pending, perr := s.store.HasPendingWikiCandidate(conceptID)
			if perr == nil && !pending {
				lr := &activation.LearningResult{
					Action:     activation.ActionWikiCandidate,
					ObjectType: activation.ObjectTypeWikiPage,
					ObjectID:   conceptID,
					Reason:     "人工指定主题范围检索命中：该概念的 qualifying KP 满足概念级 ready 判定",
					EventIDs:   marshalIDs(byEntry[conceptID]),
					Status:     activation.ResultPendingConfirm,
				}
				if err := s.activationSvc.Store().InsertLearningResult(lr); err != nil {
					slog.Warn("wiki: manual topic insert member wiki_candidate failed", "entry_id", conceptID, "error", err)
				}
			}
			pendingEntries = append(pendingEntries, conceptID)
			continue
		}
		uncoveredConcepts = append(uncoveredConcepts, conceptID)
	}
	sort.Strings(publishedMembers)

	relatedCount, err := s.store.CountRelationEdgesWithin(publishedMembers, RelationRelated)
	if err != nil {
		return nil, nil, fmt.Errorf("wiki: manual topic count related edges: %w", err)
	}
	contradictsCount, err := s.store.CountRelationEdgesWithin(publishedMembers, RelationContradicts)
	if err != nil {
		return nil, nil, fmt.Errorf("wiki: manual topic count contradicts edges: %w", err)
	}
	reliability, err := s.store.VerifiedFraction(candidateIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("wiki: manual topic reliability: %w", err)
	}
	reliabilityMin := s.cfg.TopicReliabilityMin
	if reliabilityMin <= 0 {
		reliabilityMin = 0.5
	}

	readiness := &TopicReadiness{
		MemberCount:                len(publishedMembers),
		RelatedConnectionCount:     relatedCount,
		ContradictsConnectionCount: contradictsCount,
		ReliabilityRatio:           reliability,
		ReliabilityMin:             reliabilityMin,
	}

	if len(publishedMembers) == 0 {
		return nil, readiness, &ErrInvalidTopicMembers{
			Message: "wiki: no published concept pages among this topic scope's qualifying entries yet",
		}
	}

	cand, err := s.createTopicShell(publishedMembers, relatedCount, contradictsCount, marshalIDs([]string{ManualTriggerSentinel}))
	if err != nil {
		return nil, nil, err
	}
	cand.PendingEntries = pendingEntries
	cand.UncoveredEntries = uncoveredConcepts
	cand.Reason = fmt.Sprintf("人工指定主题范围 %q，已发布成员 %d 个，related 边 %d 条，contradicts 边 %d 条，整体可靠度 %.2f；待发布成员 %d 个，未覆盖概念 %d 个",
		topicName, len(publishedMembers), relatedCount, contradictsCount, reliability, len(pendingEntries), len(uncoveredConcepts))

	slog.Info("wiki: created manual topic shell", "page_id", cand.PageID, "members", len(publishedMembers))
	return cand, readiness, nil
}

func (s *Service) createTopicShell(memberPageIDs []string, relatedCount, contradictsCount int, compiledFrom string) (*TopicCandidate, error) {
	var titles []string
	for _, id := range memberPageIDs {
		p, err := s.store.GetPage(id)
		if err != nil || p == nil {
			continue
		}
		titles = append(titles, p.Title)
	}
	placeholderTitle := strings.Join(titles, " / ")
	if compiledFrom == "" {
		compiledFrom = "[]"
	}

	shell := &Page{
		PageID:        uuid.New().String(),
		PageType:      PageTypeTopic,
		Title:         placeholderTitle,
		Content:       "",
		Status:        StatusDraft,
		CompiledFrom:  compiledFrom,
		PromptVersion: "",
		ModelName:     "",
	}
	if err := s.store.InsertPage(shell); err != nil {
		return nil, fmt.Errorf("wiki: insert topic shell page: %w", err)
	}
	for _, m := range memberPageIDs {
		if err := s.store.UpsertPageRelation(shell.PageID, m, RelationContains, DerivedFromCompile, "{}"); err != nil {
			return nil, fmt.Errorf("wiki: insert contains row: %w", err)
		}
	}

	return &TopicCandidate{
		PageID: shell.PageID, MemberPageIDs: memberPageIDs,
		RelatedCount: relatedCount, ContradictsCount: contradictsCount,
	}, nil
}

// RejectTopicCandidate implements docs/impl/v1/wiki.md 步骤 8 "人工驳回":
// archive the shell page and delete its contains rows so no dangling shell
// is left behind. The learning_result itself is resolved by the caller
// (activation.Store.ResolvePending), same convention as other pending
// confirmations.
func (s *Service) RejectTopicCandidate(pageID string) error {
	page, err := s.store.GetPage(pageID)
	if err != nil {
		return err
	}
	if page == nil {
		return ErrPageNotFound
	}
	if err := s.store.UpdatePageStatus(pageID, StatusArchived); err != nil {
		return err
	}
	members, err := s.store.ContainsMembers(pageID)
	if err != nil {
		slog.Warn("wiki: list contains members on reject failed", "page_id", pageID, "error", err)
	}
	for _, m := range members {
		if err := s.store.DeleteContainsRow(pageID, m); err != nil {
			slog.Warn("wiki: delete contains row on reject failed", "page_id", pageID, "member", m, "error", err)
		}
	}
	slog.Info("wiki: rejected topic page candidate", "page_id", pageID)
	return nil
}

// requireAllMembersPublished implements the topic compile/recompile
// precondition (docs/impl/v1/wiki.md 步骤 8/9): "人工先子后父" — every
// contains member must be status=published, else 409 listing the pending
// ones.
type ErrMembersNotPublished struct {
	Pending map[string]string // page_id -> status
}

func (e *ErrMembersNotPublished) Error() string {
	return fmt.Sprintf("wiki: %d contains member(s) not published", len(e.Pending))
}

func (s *Service) requireAllMembersPublished(topicPageID string) ([]string, error) {
	members, err := s.store.ContainsMembers(topicPageID)
	if err != nil {
		return nil, fmt.Errorf("wiki: list contains members: %w", err)
	}
	pending := make(map[string]string)
	for _, m := range members {
		p, err := s.store.GetPage(m)
		if err != nil {
			return nil, fmt.Errorf("wiki: get member page: %w", err)
		}
		if p == nil {
			continue
		}
		if p.Status != StatusPublished {
			pending[m] = p.Status
		}
	}
	if len(pending) > 0 {
		return nil, &ErrMembersNotPublished{Pending: pending}
	}
	return members, nil
}

// topicCompileInputs bundles the second-tier compile's material-gathering
// step (docs/impl/v1/wiki.md 步骤 8 "输入收集"): member pages, not KPs/KUs.
type topicCompileInputs struct {
	memberPageIDs   []string
	memberTitles    map[string]string
	materials       string
	relationsText   string
	whitelistPoints map[string]bool
}

func (s *Service) gatherTopicInputs(topicPageID string) (*topicCompileInputs, error) {
	memberPageIDs, err := s.requireAllMembersPublished(topicPageID)
	if err != nil {
		return nil, err
	}
	if len(memberPageIDs) == 0 {
		return nil, fmt.Errorf("wiki: topic page %s has no contains members", topicPageID)
	}

	maxChars := s.cfg.TopicCompileMaxChars
	if maxChars <= 0 {
		maxChars = 24000
	}

	type memberInfo struct {
		page  *Page
		count int
	}
	var members []memberInfo
	for _, id := range memberPageIDs {
		p, err := s.store.GetPage(id)
		if err != nil || p == nil {
			continue
		}
		var pointIDs []string
		json.Unmarshal([]byte(p.SourcePointIDs), &pointIDs)
		members = append(members, memberInfo{page: p, count: len(pointIDs)})
	}
	sort.SliceStable(members, func(i, j int) bool { return members[i].count > members[j].count })

	whitelist := make(map[string]bool)
	titles := make(map[string]string, len(members))
	var sb strings.Builder
	totalChars := 0
	var included []string
	for _, m := range members {
		titles[m.page.PageID] = m.page.Title
		block := fmt.Sprintf("【成员页面：%s（page_id=%s）】\n%s\n\n", m.page.Title, m.page.PageID, m.page.Content)
		n := len([]rune(block))
		if totalChars+n > maxChars && len(included) > 0 {
			continue
		}
		sb.WriteString(block)
		totalChars += n
		included = append(included, m.page.PageID)

		var pointIDs []string
		json.Unmarshal([]byte(m.page.SourcePointIDs), &pointIDs)
		for _, pid := range pointIDs {
			whitelist[pid] = true
		}
	}

	var relSb strings.Builder
	for _, id := range included {
		rels, err := s.store.ListPageRelations(id)
		if err != nil {
			continue
		}
		for _, r := range rels {
			other := r.ToPageID
			if other == id {
				other = r.FromPageID
			}
			if titles[other] == "" || r.RelationType == RelationContains {
				continue
			}
			fmt.Fprintf(&relSb, "%s 与 %s：%s\n", titles[id], titles[other], r.RelationType)
		}
	}

	return &topicCompileInputs{
		memberPageIDs:   included,
		memberTitles:    titles,
		materials:       sb.String(),
		relationsText:   relSb.String(),
		whitelistPoints: whitelist,
	}, nil
}

// AnalyzeTopic implements docs/impl/v1/wiki.md 步骤 8:
// POST /wiki/pages/:id/topic/analyze. Not persisted, mirrors Analyze.
func (s *Service) AnalyzeTopic(ctx context.Context, topicPageID string) (*AnalyzeResult, error) {
	page, err := s.store.GetPage(topicPageID)
	if err != nil {
		return nil, err
	}
	if page == nil {
		return nil, ErrPageNotFound
	}
	if page.PageType != PageTypeTopic {
		return nil, fmt.Errorf("wiki: page %s is not a topic page", topicPageID)
	}
	if page.Status != StatusDraft && page.Status != StatusNeedsRecompile {
		return nil, fmt.Errorf("%w: topic analyze only valid from draft/needs_recompile", ErrInvalidStateTransition)
	}

	claims, tensions, err := s.analyzeTopicClaims(ctx, topicPageID)
	if err != nil {
		return nil, err
	}
	return &AnalyzeResult{PageType: PageTypeTopic, Claims: claims, Tensions: tensions}, nil
}

func (s *Service) analyzeTopicClaims(ctx context.Context, topicPageID string) ([]Claim, []Tension, error) {
	in, err := s.gatherTopicInputs(topicPageID)
	if err != nil {
		return nil, nil, err
	}

	vars := map[string]string{
		"materials": in.materials,
		"relations": in.relationsText,
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		raw, err := s.llmClient.CompleteJSON(ctx, "wiki_topic_analyze.md", vars, "reasoning")
		if err != nil {
			lastErr = fmt.Errorf("llm call: %w", err)
			slog.Warn("wiki: topic analyze llm call failed", "attempt", attempt, "page_id", topicPageID, "error", err)
			continue
		}

		var output struct {
			Claims   []Claim   `json:"claims"`
			Tensions []Tension `json:"tensions"`
		}
		if err := json.Unmarshal(raw, &output); err != nil {
			lastErr = fmt.Errorf("parse: %w", err)
			slog.Warn("wiki: topic analyze parse failed", "attempt", attempt, "page_id", topicPageID, "error", err)
			continue
		}

		claims := filterClaims(output.Claims, in.whitelistPoints, topicPageID)
		tensions := filterTensions(output.Tensions, in.whitelistPoints, topicPageID)
		if len(claims) == 0 {
			lastErr = fmt.Errorf("wiki: topic analysis produced no usable claims")
			slog.Warn("wiki: topic analyze produced no usable claims", "attempt", attempt, "page_id", topicPageID)
			continue
		}
		return claims, tensions, nil
	}
	return nil, nil, fmt.Errorf("wiki: topic analyze failed after retry: %w", lastErr)
}

// CompileTopic implements docs/impl/v1/wiki.md 步骤 8:
// POST /wiki/pages/:id/topic/compile.
func (s *Service) CompileTopic(ctx context.Context, topicPageID string, claims []Claim, tensions []Tension) (*Page, error) {
	page, err := s.store.GetPage(topicPageID)
	if err != nil {
		return nil, err
	}
	if page == nil {
		return nil, ErrPageNotFound
	}
	if page.PageType != PageTypeTopic {
		return nil, fmt.Errorf("wiki: page %s is not a topic page", topicPageID)
	}

	if len(claims) == 0 {
		var err error
		claims, tensions, err = s.analyzeTopicClaims(ctx, topicPageID)
		if err != nil {
			return nil, err
		}
	}

	compiled, err := s.compileTopicContent(ctx, topicPageID, claims, tensions)
	if err != nil {
		return nil, err
	}

	if err := s.store.ReplaceContent(topicPageID, compiled.title, compiled.content,
		marshalIDs(compiled.sourcePointIDs), marshalIDs(compiled.sourceUnitIDs), marshalIDs(compiled.sourceLinkIDs),
		marshalConditions(compiled.observedConditions),
		marshalIDs(compiled.aliases), marshalIDs(compiled.triggerQuestions),
		marshalUncoveredPoints(compiled.uncoveredPoints),
		"[]", "", "[]", "v1", "reasoning"); err != nil {
		return nil, err
	}
	if err := s.store.UpdateMemberRoles(topicPageID, marshalMemberRoles(compiled.memberRoles)); err != nil {
		slog.Error("wiki: update member roles failed", "page_id", topicPageID, "error", err)
	}
	if err := s.store.InsertRevision(&Revision{PageID: topicPageID, Content: compiled.content, Reason: "topic_compile"}); err != nil {
		slog.Error("wiki: insert topic compile revision failed", "page_id", topicPageID, "error", err)
	}

	slog.Info("wiki: compiled topic page", "page_id", topicPageID, "members", len(compiled.sourcePointIDs))
	return s.store.GetPage(topicPageID)
}

// RecompileTopic implements docs/impl/v1/wiki.md 步骤 9 "主题页重编译前置检查":
// same precondition as compile (all members published), reusing the compile
// pipeline; called from Service.Recompile when page.PageType==topic.
func (s *Service) RecompileTopic(ctx context.Context, page *Page, reason string, compiledFrom []string) (*Page, error) {
	// Drop contains rows for any archived member first (docs/impl/v1/wiki.md
	// 步骤 9: "成员被 archive → 重编译时从 contains 中移除该行").
	members, err := s.store.ContainsMembers(page.PageID)
	if err != nil {
		return nil, fmt.Errorf("wiki: recompile topic: list members: %w", err)
	}
	remaining := 0
	for _, m := range members {
		mp, err := s.store.GetPage(m)
		if err != nil {
			return nil, err
		}
		if mp == nil || mp.Status == StatusArchived {
			if err := s.store.DeleteContainsRow(page.PageID, m); err != nil {
				slog.Warn("wiki: drop archived member contains row failed", "page_id", page.PageID, "member", m, "error", err)
			}
			continue
		}
		remaining++
	}
	memberMin := s.cfg.TopicMemberMin
	if memberMin <= 0 {
		memberMin = 3
	}
	if remaining < memberMin {
		return nil, fmt.Errorf("%w: topic page %s would have only %d non-archived member(s) after recompile (min %d) — archive the topic page instead",
			ErrInvalidStateTransition, page.PageID, remaining, memberMin)
	}

	claims, tensions, err := s.analyzeTopicClaims(ctx, page.PageID)
	if err != nil {
		return nil, err
	}
	compiled, err := s.compileTopicContent(ctx, page.PageID, claims, tensions)
	if err != nil {
		return nil, err
	}

	if err := s.store.ReplaceContent(page.PageID, compiled.title, compiled.content,
		marshalIDs(compiled.sourcePointIDs), marshalIDs(compiled.sourceUnitIDs), marshalIDs(compiled.sourceLinkIDs),
		marshalConditions(compiled.observedConditions),
		marshalIDs(compiled.aliases), marshalIDs(compiled.triggerQuestions),
		marshalUncoveredPoints(compiled.uncoveredPoints),
		marshalIDs(compiledFrom), "", "[]", "v1", "reasoning"); err != nil {
		return nil, err
	}
	if err := s.store.UpdateMemberRoles(page.PageID, marshalMemberRoles(compiled.memberRoles)); err != nil {
		slog.Error("wiki: update member roles failed", "page_id", page.PageID, "error", err)
	}
	if err := s.store.InsertRevision(&Revision{PageID: page.PageID, Content: compiled.content, Reason: reason}); err != nil {
		slog.Error("wiki: insert topic recompile revision failed", "page_id", page.PageID, "error", err)
	}
	if err := s.wikiIndex.Delete(page.PageID); err != nil {
		slog.Warn("wiki: remove topic page from index after recompile failed", "page_id", page.PageID, "error", err)
	}

	slog.Info("wiki: recompiled topic page", "page_id", page.PageID, "reason", reason)
	return s.store.GetPage(page.PageID)
}

type compiledTopicContent struct {
	compiledContent
	memberRoles []MemberRole
}

// compileTopicContent implements docs/impl/v1/wiki.md 步骤 8 "生成阶段" +
// "生成后校验". source_link_ids/observed_conditions/uncovered_points are the
// union of member pages' same-named fields (not re-queried from
// activation_links — the member pages already did that at their own compile
// time, so the four-tuple entry is naturally inherited).
func (s *Service) compileTopicContent(ctx context.Context, topicPageID string, claims []Claim, tensions []Tension) (*compiledTopicContent, error) {
	if len(claims) == 0 {
		return nil, fmt.Errorf("wiki: no confirmed claims for topic page %s", topicPageID)
	}

	in, err := s.gatherTopicInputs(topicPageID)
	if err != nil {
		return nil, err
	}

	whitelist := claimsWhitelist(claims)

	claimsJSON, _ := json.Marshal(claims)
	tensionsJSON := "[]"
	if len(tensions) > 0 {
		if b, err := json.Marshal(tensions); err == nil {
			tensionsJSON = string(b)
		}
	}

	var memberPagesText strings.Builder
	for _, id := range in.memberPageIDs {
		fmt.Fprintf(&memberPagesText, "%s: %s\n", id, in.memberTitles[id])
	}

	vars := map[string]string{
		"claims":       string(claimsJSON),
		"tensions":     tensionsJSON,
		"member_pages": memberPagesText.String(),
		"materials":    in.materials,
	}

	triggerMax := s.cfg.TriggerQuestionsMax
	if triggerMax <= 0 {
		triggerMax = 10
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		raw, err := s.llmClient.CompleteJSON(ctx, "wiki_topic_compile.md", vars, "reasoning")
		if err != nil {
			lastErr = fmt.Errorf("llm call: %w", err)
			slog.Warn("wiki: topic compile llm call failed", "attempt", attempt, "page_id", topicPageID, "error", err)
			continue
		}

		var output struct {
			Title            string       `json:"title"`
			Content          string       `json:"content"`
			CitedPointIDs    []string     `json:"cited_point_ids"`
			Aliases          []string     `json:"aliases"`
			TriggerQuestions []string     `json:"trigger_questions"`
			MemberRoles      []MemberRole `json:"member_roles"`
		}
		if err := json.Unmarshal(raw, &output); err != nil {
			lastErr = fmt.Errorf("parse: %w", err)
			slog.Warn("wiki: topic compile parse failed", "attempt", attempt, "page_id", topicPageID, "error", err)
			continue
		}

		filteredContent, citedInContent, stripped := filterContentTags(output.Content, whitelist)
		if len(stripped) > 0 {
			slog.Warn("wiki: stripped out-of-whitelist point_id tags from topic content", "page_id", topicPageID, "ids", stripped)
		}
		if !hasRequiredTopicSections(filteredContent) {
			lastErr = fmt.Errorf("wiki: compiled topic content missing required sections")
			slog.Warn("wiki: topic compile missing required sections", "attempt", attempt, "page_id", topicPageID)
			continue
		}
		if strings.TrimSpace(output.Title) == "" {
			lastErr = fmt.Errorf("wiki: compiled topic title is empty")
			slog.Warn("wiki: topic compile empty title", "attempt", attempt, "page_id", topicPageID)
			continue
		}

		memberSet := make(map[string]bool, len(in.memberPageIDs))
		for _, id := range in.memberPageIDs {
			memberSet[id] = true
		}
		var memberRoles []MemberRole
		var droppedRoles []string
		for _, mr := range output.MemberRoles {
			if !memberSet[mr.MemberPageID] {
				droppedRoles = append(droppedRoles, mr.MemberPageID)
				continue
			}
			memberRoles = append(memberRoles, mr)
		}
		if len(droppedRoles) > 0 {
			slog.Warn("wiki: dropped out-of-membership member_roles", "page_id", topicPageID, "ids", droppedRoles)
		}
		if len(memberRoles) == 0 {
			slog.Warn("wiki: topic compile output missing member_roles, storing empty", "page_id", topicPageID)
		}

		unionPointIDs, unionUnitIDs, unionLinkIDs, unionConditions, unionUncovered := s.unionMemberFields(in.memberPageIDs)
		// citedInContent is a subset check; the actual source_point_ids stored
		// is the union of member fields per 步骤 8's spec, but must still only
		// contain ids the content actually cites — intersect with citedInContent
		// to keep the citation-whitelist invariant meaningful for topic pages too.
		cited := make(map[string]bool, len(citedInContent))
		for _, id := range citedInContent {
			cited[id] = true
		}
		var sourcePointIDs []string
		for _, id := range unionPointIDs {
			if cited[id] {
				sourcePointIDs = append(sourcePointIDs, id)
			}
		}

		if len(output.Aliases) == 0 || len(output.TriggerQuestions) == 0 {
			slog.Warn("wiki: topic compile output missing aliases/trigger_questions, storing empty", "page_id", topicPageID)
		}

		return &compiledTopicContent{
			compiledContent: compiledContent{
				title:              output.Title,
				content:            filteredContent,
				sourcePointIDs:     sourcePointIDs,
				sourceUnitIDs:      unionUnitIDs,
				sourceLinkIDs:      unionLinkIDs,
				observedConditions: unionConditions,
				aliases:            truncateStrings(output.Aliases, triggerMax),
				triggerQuestions:   truncateStrings(output.TriggerQuestions, triggerMax),
				uncoveredPoints:    unionUncovered,
			},
			memberRoles: memberRoles,
		}, nil
	}

	return nil, fmt.Errorf("wiki: topic compile failed after retry: %w", lastErr)
}

// unionMemberFields implements docs/impl/v1/wiki.md 步骤 8's
// source_link_ids/observed_conditions/uncovered_points union rule: these are
// taken directly from member pages' already-computed fields, not re-queried
// against activation_links (the members already did that at their own
// compile time).
func (s *Service) unionMemberFields(memberPageIDs []string) (pointIDs, unitIDs, linkIDs []string, conditions []activation.ObservedCondition, uncovered []UncoveredPoint) {
	seenPoint, seenUnit, seenLink, seenUncovered := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, id := range memberPageIDs {
		p, err := s.store.GetPage(id)
		if err != nil || p == nil {
			continue
		}
		var pts, units, links []string
		var conds []activation.ObservedCondition
		var unc []UncoveredPoint
		json.Unmarshal([]byte(p.SourcePointIDs), &pts)
		json.Unmarshal([]byte(p.SourceUnitIDs), &units)
		json.Unmarshal([]byte(p.SourceLinkIDs), &links)
		json.Unmarshal([]byte(p.ObservedConditions), &conds)
		json.Unmarshal([]byte(p.UncoveredPoints), &unc)

		for _, id := range pts {
			if !seenPoint[id] {
				seenPoint[id] = true
				pointIDs = append(pointIDs, id)
			}
		}
		for _, id := range units {
			if !seenUnit[id] {
				seenUnit[id] = true
				unitIDs = append(unitIDs, id)
			}
		}
		for _, id := range links {
			if !seenLink[id] {
				seenLink[id] = true
				linkIDs = append(linkIDs, id)
			}
		}
		for _, c := range conds {
			conditions = activation.MergeObservedConditions(conditions, c, 0)
		}
		for _, u := range unc {
			if !seenUncovered[u.PointID] {
				seenUncovered[u.PointID] = true
				uncovered = append(uncovered, u)
			}
		}
	}
	return
}

func hasRequiredTopicSections(content string) bool {
	if strings.TrimSpace(content) == "" {
		return false
	}
	for _, h := range requiredTopicSections {
		if !strings.Contains(content, h) {
			return false
		}
	}
	return true
}

func marshalMemberRoles(roles []MemberRole) string {
	if len(roles) == 0 {
		return "[]"
	}
	b, err := json.Marshal(roles)
	if err != nil {
		return "[]"
	}
	return string(b)
}
