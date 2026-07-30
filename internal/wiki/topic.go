// Topic-page candidate detection and second-tier compilation
// (docs/impl/v1/wiki.md 步骤 8).
package wiki

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/activation"
)

var requiredTopicSections = []string{"## 主题概览", "## 主线结论", "## 子主题分工", "## 跨主题矛盾与待验证点", "## 依赖页面"}

// TopicCandidate is one connected component that qualified for a topic-page
// shell (docs/impl/v1/wiki.md 步骤 8 "候选产生").
type TopicCandidate struct {
	PageID           string   // the newly created draft shell page
	MemberPageIDs    []string
	RelatedCount     int
	ContradictsCount int
	Reason           string
}

// OversizedCluster is a connected component that exceeded topic_member_max —
// report-only, never auto-split (docs/impl/v1/wiki.md 步骤 8, study.md 步骤
// 6 "报告提示项").
type OversizedCluster struct {
	MemberCount           int
	RepresentativePageIDs []string
	RelatedCount          int
	ContradictsCount      int
}

// unionFind is a minimal disjoint-set used to build connected components
// from related edges among published concept pages.
type unionFind struct {
	parent map[string]string
}

func newUnionFind(nodes []string) *unionFind {
	u := &unionFind{parent: make(map[string]string, len(nodes))}
	for _, n := range nodes {
		u.parent[n] = n
	}
	return u
}

func (u *unionFind) find(x string) string {
	if _, ok := u.parent[x]; !ok {
		u.parent[x] = x
		return x
	}
	for u.parent[x] != x {
		u.parent[x] = u.parent[u.parent[x]]
		x = u.parent[x]
	}
	return x
}

func (u *unionFind) union(a, b string) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[ra] = rb
	}
}

// DetectTopicCandidates implements docs/impl/v1/wiki.md 步骤 8 "候选产生":
// build the related-edge graph among published concept pages, take connected
// components, and for every component satisfying the member-count range,
// coherence (contradicts < related), and "enough uncontained members"
// conditions, create a draft topic shell page + contains rows in one
// transaction. Called by Study's periodic scan
// (docs/impl/v1/study.md 步骤 6); Study writes the topic_page_candidate
// learning_results using the returned TopicCandidate list, and the
// oversized_topic_cluster report item using the returned OversizedCluster list.
func (s *Service) DetectTopicCandidates() ([]TopicCandidate, []OversizedCluster, error) {
	pages, err := s.store.ListPublishedConceptPagesWithPoints()
	if err != nil {
		return nil, nil, fmt.Errorf("wiki: detect topic candidates: list pages: %w", err)
	}
	if len(pages) == 0 {
		return nil, nil, nil
	}

	nodes := make([]string, 0, len(pages))
	for _, p := range pages {
		nodes = append(nodes, p.PageID)
	}
	uf := newUnionFind(nodes)

	edges, err := s.store.ListRelatedEdges()
	if err != nil {
		return nil, nil, fmt.Errorf("wiki: detect topic candidates: list related edges: %w", err)
	}
	for _, e := range edges {
		uf.union(e.FromPageID, e.ToPageID)
	}

	components := make(map[string][]string)
	for _, n := range nodes {
		root := uf.find(n)
		components[root] = append(components[root], n)
	}

	memberMin := s.cfg.TopicMemberMin
	if memberMin <= 0 {
		memberMin = 3
	}
	memberMax := s.cfg.TopicMemberMax
	if memberMax <= 0 {
		memberMax = 8
	}

	var candidates []TopicCandidate
	var oversized []OversizedCluster

	for _, members := range components {
		sort.Strings(members)
		if len(members) < memberMin {
			continue
		}

		relatedCount, err := s.store.CountRelationEdgesWithin(members, RelationRelated)
		if err != nil {
			slog.Error("wiki: count related edges within component failed", "error", err)
			continue
		}
		contradictsCount, err := s.store.CountRelationEdgesWithin(members, RelationContradicts)
		if err != nil {
			slog.Error("wiki: count contradicts edges within component failed", "error", err)
			continue
		}

		if len(members) > memberMax {
			repr := members
			if len(repr) > 5 {
				repr = repr[:5]
			}
			oversized = append(oversized, OversizedCluster{
				MemberCount: len(members), RepresentativePageIDs: repr,
				RelatedCount: relatedCount, ContradictsCount: contradictsCount,
			})
			continue
		}

		if contradictsCount >= relatedCount {
			continue
		}

		uncontainedCount := 0
		for _, m := range members {
			topics, err := s.store.ContainingTopics(m)
			if err != nil {
				slog.Warn("wiki: check containing topics failed", "page_id", m, "error", err)
				continue
			}
			if len(topics) == 0 {
				uncontainedCount++
			}
		}
		if uncontainedCount < memberMin {
			continue
		}

		cand, err := s.createTopicShell(members, relatedCount, contradictsCount)
		if err != nil {
			slog.Error("wiki: create topic shell failed", "error", err)
			continue
		}
		candidates = append(candidates, *cand)
	}

	return candidates, oversized, nil
}

func (s *Service) createTopicShell(memberPageIDs []string, relatedCount, contradictsCount int) (*TopicCandidate, error) {
	var titles []string
	for _, id := range memberPageIDs {
		p, err := s.store.GetPage(id)
		if err != nil || p == nil {
			continue
		}
		titles = append(titles, p.Title)
	}
	placeholderTitle := strings.Join(titles, " / ")

	shell := &Page{
		PageID:        uuid.New().String(),
		PageType:      PageTypeTopic,
		Title:         placeholderTitle,
		Content:       "",
		Status:        StatusDraft,
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

	reason := fmt.Sprintf("连通分量 %d 个成员，related 边 %d 条，contradicts 边 %d 条", len(memberPageIDs), relatedCount, contradictsCount)
	return &TopicCandidate{
		PageID: shell.PageID, MemberPageIDs: memberPageIDs,
		RelatedCount: relatedCount, ContradictsCount: contradictsCount, Reason: reason,
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
		"[]", "v1", "reasoning"); err != nil {
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
		marshalIDs(compiledFrom), "v1", "reasoning"); err != nil {
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
