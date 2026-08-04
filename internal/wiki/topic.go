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
	PageID            string   // the newly created draft shell page
	MemberPageIDs     []string // published members with a contains row
	PendingEntries   []string // entry_id list that cleared 步骤3 ready but has no published page yet — "待发布成员", a wiki_candidate was written and will get a contains row once it publishes (步骤 7b)
	UncoveredEntries []string // entry_id list that did NOT clear 步骤3 ready — 缺材料, no wiki_candidate written
	RelatedCount      int
	ContradictsCount  int
	Reason            string
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
			if hit.Score < s.topicSearchMinScore {
				continue
			}
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
	in, err := s.gatherAnalyzeInputs(conceptID)
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

// CreateTopicManual implements docs/impl/v1/wiki.md 步骤 8 "人工手动指定
// 主题" (2026-08-03 修订): the human gives a topic *scope* (name +
// description [+ domain]), not a member-page list — the same candidate-range
// retrieval / qualifying filter / concept grouping as Study's automatic path
// (步骤 8 第 3-6 步), just triggered manually and with admission (步骤 7)
// shown rather than enforced. No topic_page_candidate learning_result is
// written (no pending_confirm object to resolve — the shell itself can be
// archived directly to reject it).
func (s *Service) CreateTopicManual(topicName, topicDescription, domainID string) (*TopicCandidate, *TopicReadiness, error) {
	if strings.TrimSpace(topicName) == "" {
		return nil, nil, &ErrInvalidTopicMembers{Message: "wiki: topic_name is required"}
	}
	queryText := strings.TrimSpace(topicName + " " + topicDescription)

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
			return nil, nil, fmt.Errorf("wiki: manual topic candidate search: %w", err)
		}
		for _, hit := range res.Hits {
			if hit.Score < s.topicSearchMinScore {
				continue
			}
			if domainID != "" {
				d, err := s.store.PointDomainID(hit.ID)
				if err == nil && d != domainID {
					continue
				}
			}
			candidateIDs = append(candidateIDs, hit.ID)
		}
	}
	if len(candidateIDs) > kpMax {
		candidateIDs = candidateIDs[:kpMax]
	}

	qualifying, err := s.store.QualifyingPointsByIDs(candidateIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("wiki: manual topic qualifying filter: %w", err)
	}
	if len(qualifying) == 0 {
		return nil, nil, &ErrInvalidTopicMembers{Message: "wiki: no current knowledge points found for this topic scope"}
	}

	byEntry := make(map[string][]string)
	var conceptOrder []string
	for _, q := range qualifying {
		if _, ok := byEntry[q.EntryID]; !ok {
			conceptOrder = append(conceptOrder, q.EntryID)
		}
		byEntry[q.EntryID] = append(byEntry[q.EntryID], q.PointID)
	}
	sort.Strings(conceptOrder)

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
