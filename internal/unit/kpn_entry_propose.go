package unit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"unicode"
)

// conceptProposeBatchMaxSize caps how many orphan KPs go into one
// kpn_entry_propose.md LLM call (docs/impl/v1/kpn.md 步骤 3), mirroring
// crossBatchMaxSize.
const conceptProposeBatchMaxSize = 60

// conceptProposeCluster is kpn_entry_propose.md's per-cluster output —
// clustering and naming only. Kind is forced to "concept" by
// proposeConceptClustersFromOrphans (fact leftovers take the dedicated
// concept-dimension path instead, never this freeform call).
type conceptProposeCluster struct {
	SuggestedName        string `json:"suggested_name"`
	SuggestedDescription string `json:"suggested_description"`
	// SuggestedBoundary is kpn_entry_propose.md's suggested_boundary
	// (2026-08-05 schema addition) — carried through to entries.boundary at
	// confirm time so LLM-proposed ("evolved") entries get the same
	// disambiguation signal preset entries already have.
	SuggestedBoundary string `json:"suggested_boundary"`
	// Kind is set by the caller (concept path forces "concept").
	Kind     string
	PointIDs []string `json:"point_ids"`
}

type conceptProposeOutput struct {
	Clusters []conceptProposeCluster `json:"clusters"`
}

// orphanFactMatch/orphanFactMatchOutput are kpn_orphan_fact_match.md's output
// shape (2026-08-05 修订，见 docs/impl/v1/kpn.md 步骤 3): classifies each RAW
// orphan point directly against the domain's existing concept list, before
// any clustering happens. PointIndex indexes back into the batch slice passed
// to that call (not the point_id string — same hallucination-avoidance
// pattern as cluster_index elsewhere).
type orphanFactMatch struct {
	PointIndex       int     `json:"point_index"`
	MatchedConceptID *string `json:"matched_concept_id"`
}

type orphanFactMatchOutput struct {
	Matches []orphanFactMatch `json:"matches"`
}

// factGroupEntityOutput is kpn_fact_group_entity.md's output shape: the one
// common entity (and its type) that an entire (source, matched-concept) group
// of points is about — inferred from the group as a whole (titles, summaries,
// content together), not from any single point in isolation. Empty Entity
// means the call found no coherent shared entity; the caller drops the group.
// Description/Boundary are this specific entity+concept combination's own
// description/boundary (2026-08-05 修订) — without them ProposeAddCandidate
// got called with empty strings, so entries confirmed from these candidates
// ended up with no description/boundary at all, unlike every other entry in
// the system (preset or freeform-clustered).
type factGroupEntityOutput struct {
	Entity      string `json:"entity"`
	EntityType  string `json:"entity_type"`
	Description string `json:"description"`
	Boundary    string `json:"boundary"`
}

// ProposeEntriesForDomainOrphans is the 知识领域页面"+ 新增词条"按钮的入口
// (on-demand variant of docs/impl/v1/kpn.md 步骤 3, otherwise only run
// automatically at the end of each Source's unit_extract): classifies every
// standing entry_id-empty KP already in domainID, prefers same-kind matches
// onto existing entries, then creates pending_confirm candidates for the
// leftovers (fact → entity+concept; concept → freeform name). Returns how
// many (cluster, source) proposals were written.
func (s *Service) ProposeEntriesForDomainOrphans(ctx context.Context, domainID string) (int, error) {
	if s.conceptNotifier == nil {
		return 0, nil
	}
	orphans, err := s.store.GetOrphanPointsByDomain(domainID)
	if err != nil {
		return 0, fmt.Errorf("unit: propose entries for domain orphans: get points: %w", err)
	}
	if len(orphans) == 0 {
		return 0, nil
	}

	unitIDs := make([]string, 0, len(orphans))
	for _, p := range orphans {
		unitIDs = append(unitIDs, p.UnitID)
	}
	unitCenterMap, err := s.store.GetUnitCentersByIDs(unitIDs)
	if err != nil {
		return 0, fmt.Errorf("unit: propose entries for domain orphans: get unit centers: %w", err)
	}

	return s.proposeEntriesForOrphans(ctx, "", domainID, orphans, unitCenterMap)
}

// proposeEntriesForOrphans implements docs/impl/v1/kpn.md 步骤 3 (classify-
// then-branch, aligned with unit_extract's direct-match path):
//  1. drop KPs already on pending_confirm add candidates
//  2. classify each orphan KU as concept/fact (unit_kind_classify.md)
//  3. prefer same-kind match onto existing entries (unit_entry_match.md →
//     write entry_id, no candidate)
//  4. remaining fact KPs → attach to a domain concept dimension, infer one
//     shared entity, write fact candidate named entity+concept
//  5. remaining concept KPs → freeform cluster/name as concept candidates
// Fact leftovers that cannot attach to any concept stay orphan (no bare-
// entity auto-create). orphans may span multiple Sources (domain-wide
// trigger); each cluster's point_ids are still split by SourceID before
// ProposeAddCandidate. Failures degrade per-chunk.
func (s *Service) proposeEntriesForOrphans(ctx context.Context, logSourceID, domainID string, orphans []KnowledgePoint, unitCenterMap map[string]string) (int, error) {
	if s.conceptNotifier == nil || len(orphans) == 0 {
		return 0, nil
	}

	if pendingIDs, err := s.conceptNotifier.ListPendingAddPointIDs(domainID); err != nil {
		slog.Warn("unit: kpn orphan propose list pending points failed", "domain_id", domainID, "error", err)
	} else if len(pendingIDs) > 0 {
		exclude := make(map[string]bool, len(pendingIDs))
		for _, id := range pendingIDs {
			exclude[id] = true
		}
		orphans = filterOrphansExcluding(orphans, exclude)
		if len(orphans) == 0 {
			return 0, nil
		}
	}

	existingRefs, err := s.conceptNotifier.ListActiveEntryReferences(domainID)
	if err != nil {
		slog.Warn("unit: kpn concept propose existing entries lookup failed", "domain_id", domainID, "error", err)
	}
	existingNamesText := strings.Join(existingRefs, "\n")

	titleSummaryBySource := make(map[string][2]string)
	for _, p := range orphans {
		if _, ok := titleSummaryBySource[p.SourceID]; ok {
			continue
		}
		title, summary, err := s.store.GetSourceTitleSummary(p.SourceID)
		if err != nil {
			slog.Warn("unit: kpn concept propose source title/summary lookup failed", "source_id", p.SourceID, "error", err)
		}
		titleSummaryBySource[p.SourceID] = [2]string{title, summary}
	}

	kindByUnit := s.matchOrphanUnitsToExistingEntries(ctx, domainID, orphans, titleSummaryBySource)

	// Drop points whose KU was just assigned an entry_id by same-kind match.
	unitIDs := uniqueUnitIDs(orphans)
	units, err := s.store.GetUnitsByIDs(unitIDs)
	if err != nil {
		slog.Warn("unit: kpn orphan propose reload units after match failed", "domain_id", domainID, "error", err)
	}
	assignedEntryByUnit := make(map[string]string, len(units))
	for _, u := range units {
		if u.EntryID.Valid && u.EntryID.String != "" {
			assignedEntryByUnit[u.UnitID] = u.EntryID.String
		}
	}

	var factOrphans, conceptOrphans []KnowledgePoint
	pointIDsByAssignedEntry := make(map[string][]string)
	for _, p := range orphans {
		if entryID, ok := assignedEntryByUnit[p.UnitID]; ok {
			pointIDsByAssignedEntry[entryID] = append(pointIDsByAssignedEntry[entryID], p.PointID)
			continue
		}
		if kindByUnit[p.UnitID] == "fact" {
			factOrphans = append(factOrphans, p)
		} else {
			conceptOrphans = append(conceptOrphans, p)
		}
	}

	// 幂等性修复根因一：同类直接匹配（不经候选确认）当场就把 entry_id 写回
	// 了这批点所在的 KU，但本次调用一开始做跨 Source 分组时用的还是旧的
	// entry_id 快照，这批点不会被本次调用的 CrossSourceKPN 主流程处理到。
	// 复用 RematchPoints（原本只在人工确认新建候选时触发）在同一次调用内
	// 立即把它们补上一次真正的跨 Source 匹配，不用等下一次外部触发才发现。
	for entryID, pointIDs := range pointIDsByAssignedEntry {
		s.RematchPoints(entryID, pointIDs)
	}

	touched := 0
	if len(factOrphans) > 0 {
		matchedGroups, unmatchedFact, err := s.matchOrphansToExistingConcepts(ctx, domainID, factOrphans, unitCenterMap, titleSummaryBySource)
		if err != nil {
			slog.Warn("unit: kpn orphan fact match failed", "domain_id", domainID, "error", err)
			unmatchedFact = factOrphans
		} else {
			touched += s.writeFactGroupCandidates(ctx, domainID, matchedGroups, unitCenterMap, titleSummaryBySource)
		}
		// Unmatched fact KPs stay orphan — no freeform / bare-entity create.
		if len(unmatchedFact) > 0 {
			slog.Info("unit: kpn orphan fact leftovers stay unassigned",
				"domain_id", domainID, "count", len(unmatchedFact))
		}
	}

	if len(conceptOrphans) > 0 {
		touched += s.proposeConceptClustersFromOrphans(ctx, domainID, conceptOrphans, unitCenterMap, titleSummaryBySource, existingNamesText)
	}

	slog.Info("unit: kpn orphan concept proposal done",
		"source_id", logSourceID, "domain_id", domainID,
		"orphan_points", len(orphans), "fact_remaining", len(factOrphans),
		"concept_remaining", len(conceptOrphans), "proposals_touched", touched)
	return touched, nil
}

// matchOrphanUnitsToExistingEntries is the classify-then-branch same-kind
// match shared with unit_extract's matchEntries: decide each orphan KU's
// concept/fact kind, then only match against same-kind existing entries
// (writing entry_id on hit). Returns unit_id → kind for subsequent leftover
// routing; missing/failed classifications default to "concept" at the caller.
func (s *Service) matchOrphanUnitsToExistingEntries(ctx context.Context, domainID string, orphans []KnowledgePoint, titleSummaryBySource map[string][2]string) map[string]string {
	kindByUnit := make(map[string]string)

	entries, err := s.store.GetEntriesByDomainID(domainID)
	if err != nil {
		slog.Warn("unit: kpn orphan same-kind match list entries failed", "domain_id", domainID, "error", err)
		return kindByUnit
	}
	var conceptEntries, factEntries []Concept
	for _, c := range entries {
		if c.Kind == "fact" {
			factEntries = append(factEntries, c)
		} else {
			conceptEntries = append(conceptEntries, c)
		}
	}
	conceptList := renderEntryList(conceptEntries)
	factList := renderEntryList(factEntries)

	unitIDsBySource := make(map[string][]string)
	seenUnit := make(map[string]bool)
	for _, p := range orphans {
		if seenUnit[p.UnitID] {
			continue
		}
		seenUnit[p.UnitID] = true
		unitIDsBySource[p.SourceID] = append(unitIDsBySource[p.SourceID], p.UnitID)
	}

	for sourceID, unitIDs := range unitIDsBySource {
		units, err := s.store.GetUnitsByIDs(unitIDs)
		if err != nil {
			slog.Warn("unit: kpn orphan same-kind match get units failed", "source_id", sourceID, "error", err)
			continue
		}
		ts := titleSummaryBySource[sourceID]
		for uid, kind := range s.classifyUnitKinds(ctx, units, ts[0], ts[1]) {
			kindByUnit[uid] = kind
		}
		var conceptUnits, factUnits []KnowledgeUnit
		for _, u := range units {
			if kindByUnit[u.UnitID] == "fact" {
				factUnits = append(factUnits, u)
			} else {
				conceptUnits = append(conceptUnits, u)
			}
		}
		s.matchConceptBatches(ctx, conceptUnits, conceptList, ts[0], ts[1])
		s.matchConceptBatches(ctx, factUnits, factList, ts[0], ts[1])
	}
	return kindByUnit
}

// proposeConceptClustersFromOrphans freeform-clusters concept-classified
// leftover KPs and writes them as kind=concept candidates (new-concept
// discovery). Kind is forced to concept — fact leftovers never reach here.
func (s *Service) proposeConceptClustersFromOrphans(ctx context.Context, domainID string, orphans []KnowledgePoint, unitCenterMap map[string]string, titleSummaryBySource map[string][2]string, existingNamesText string) int {
	touched := 0
	for start := 0; start < len(orphans); start += conceptProposeBatchMaxSize {
		end := start + conceptProposeBatchMaxSize
		if end > len(orphans) {
			end = len(orphans)
		}
		chunk := orphans[start:end]

		var text strings.Builder
		pointSourceMap := make(map[string]string, len(chunk))
		for _, p := range chunk {
			pointSourceMap[p.PointID] = p.SourceID
			ts := titleSummaryBySource[p.SourceID]
			fmt.Fprintf(&text, "%s\t%s\t%s\t%s\t%s\n", p.PointID, unitCenterMap[p.UnitID], ts[0], ts[1], p.Content)
		}

		vars := map[string]string{"points": text.String(), "existing_concepts": existingNamesText}
		data, err := s.llmClient.CompleteJSON(ctx, "kpn_entry_propose.md", vars, "extraction")
		if err != nil {
			slog.Warn("unit: kpn concept propose llm call failed", "domain_id", domainID, "error", err)
			continue
		}
		var out conceptProposeOutput
		if err := json.Unmarshal(data, &out); err != nil {
			slog.Warn("unit: kpn concept propose parse failed", "domain_id", domainID, "error", err)
			continue
		}
		for i := range out.Clusters {
			out.Clusters[i].SuggestedName = strings.TrimSpace(out.Clusters[i].SuggestedName)
			out.Clusters[i].SuggestedBoundary = strings.TrimSpace(out.Clusters[i].SuggestedBoundary)
			out.Clusters[i].Kind = "concept"
		}

		for _, cluster := range out.Clusters {
			if cluster.SuggestedName == "" {
				continue
			}
			bySource := make(map[string][]string)
			for _, pid := range cluster.PointIDs {
				if srcID, ok := pointSourceMap[pid]; ok {
					bySource[srcID] = append(bySource[srcID], pid)
				}
			}
			if len(bySource) == 0 {
				continue
			}
			for srcID, pointIDs := range bySource {
				if _, err := s.conceptNotifier.ProposeAddCandidate(domainID, cluster.SuggestedName, cluster.SuggestedDescription, cluster.SuggestedBoundary, "concept", "", pointIDs, srcID, ""); err != nil {
					slog.Warn("unit: kpn concept propose candidate write failed", "source_id", srcID, "suggested_name", cluster.SuggestedName, "error", err)
					continue
				}
			}
			touched++
		}
	}
	return touched
}

func filterOrphansExcluding(orphans []KnowledgePoint, exclude map[string]bool) []KnowledgePoint {
	if len(exclude) == 0 {
		return orphans
	}
	out := make([]KnowledgePoint, 0, len(orphans))
	for _, p := range orphans {
		if !exclude[p.PointID] {
			out = append(out, p)
		}
	}
	return out
}

func uniqueUnitIDs(orphans []KnowledgePoint) []string {
	seen := make(map[string]bool, len(orphans))
	ids := make([]string, 0, len(orphans))
	for _, p := range orphans {
		if seen[p.UnitID] {
			continue
		}
		seen[p.UnitID] = true
		ids = append(ids, p.UnitID)
	}
	return ids
}

// factGroupKey identifies one (source, matched-concept) bucket that
// matchOrphansToExistingConcepts produces — every point in the bucket matched
// the same concept from the same Source, so they're highly likely to share
// one real-world entity (usually that Source's main subject).
type factGroupKey struct {
	SourceID  string
	ConceptID string
}

// matchOrphansToExistingConcepts is docs/impl/v1/kpn.md 步骤 3 fact 新建路径:
// classifies every fact-classified leftover KP against the domain's existing
// concept list via kpn_orphan_fact_match.md. Returns points grouped by
// (source_id, matched_concept_id) — ready for writeFactGroupCandidates —
// plus the points that matched nothing (stay orphan; no bare-entity create).
func (s *Service) matchOrphansToExistingConcepts(ctx context.Context, domainID string, orphans []KnowledgePoint, unitCenterMap map[string]string, titleSummaryBySource map[string][2]string) (map[factGroupKey][]KnowledgePoint, []KnowledgePoint, error) {
	conceptRefs, err := s.conceptNotifier.ListActiveConceptEntryReferences(domainID)
	if err != nil {
		return nil, orphans, fmt.Errorf("list active concept entry references: %w", err)
	}
	if len(conceptRefs) == 0 {
		// No concepts to match against yet — fact leftovers stay orphan
		// until the domain has at least one concept dimension.
		return nil, orphans, nil
	}
	idToName := make(map[string]string, len(conceptRefs))
	var conceptsText strings.Builder
	for _, line := range conceptRefs {
		parts := strings.Split(line, "\t")
		if len(parts) >= 2 {
			idToName[parts[0]] = parts[1]
		}
		conceptsText.WriteString(line)
		conceptsText.WriteByte('\n')
	}

	groups := make(map[factGroupKey][]KnowledgePoint)
	var unmatched []KnowledgePoint

	for start := 0; start < len(orphans); start += conceptProposeBatchMaxSize {
		end := start + conceptProposeBatchMaxSize
		if end > len(orphans) {
			end = len(orphans)
		}
		chunk := orphans[start:end]

		var text strings.Builder
		for i, p := range chunk {
			ts := titleSummaryBySource[p.SourceID]
			fmt.Fprintf(&text, "%d\t%s\t%s\t%s\t%s\n", i, unitCenterMap[p.UnitID], ts[0], ts[1], p.Content)
		}

		vars := map[string]string{"points": text.String(), "concepts": conceptsText.String()}
		data, err := s.llmClient.CompleteJSON(ctx, "kpn_orphan_fact_match.md", vars, "extraction")
		if err != nil {
			slog.Warn("unit: kpn orphan fact match llm call failed", "domain_id", domainID, "error", err)
			unmatched = append(unmatched, chunk...)
			continue
		}
		var out orphanFactMatchOutput
		if err := json.Unmarshal(data, &out); err != nil {
			slog.Warn("unit: kpn orphan fact match parse failed", "domain_id", domainID, "error", err)
			unmatched = append(unmatched, chunk...)
			continue
		}

		matchedIdx := make(map[int]string, len(out.Matches))
		for _, m := range out.Matches {
			if m.PointIndex < 0 || m.PointIndex >= len(chunk) || m.MatchedConceptID == nil {
				continue
			}
			if _, ok := idToName[*m.MatchedConceptID]; !ok {
				continue // hallucinated id
			}
			matchedIdx[m.PointIndex] = *m.MatchedConceptID
		}

		for i, p := range chunk {
			conceptID, ok := matchedIdx[i]
			if !ok {
				unmatched = append(unmatched, p)
				continue
			}
			key := factGroupKey{SourceID: p.SourceID, ConceptID: conceptID}
			groups[key] = append(groups[key], p)
		}
	}

	return groups, unmatched, nil
}

// writeFactGroupCandidates is docs/impl/v1/kpn.md 步骤 3 fact 新建收尾: for
// each (source, concept) group matchOrphansToExistingConcepts produced, asks
// kpn_fact_group_entity.md for the ONE entity the whole group shares, then
// writes a single fact candidate named entity+conceptName (joinEntityConcept,
// deterministic — see its doc comment). A group with no coherent shared
// entity (empty response) is dropped, not written with a guessed name —
// same "leave it for a human" disposition as an unmatched fact leftover.
// Returns how many candidates were written.
func (s *Service) writeFactGroupCandidates(ctx context.Context, domainID string, groups map[factGroupKey][]KnowledgePoint, unitCenterMap map[string]string, titleSummaryBySource map[string][2]string) int {
	if len(groups) == 0 {
		return 0
	}

	conceptRefs, err := s.conceptNotifier.ListActiveConceptEntryReferences(domainID)
	if err != nil {
		slog.Warn("unit: kpn fact group entity: list concepts failed", "domain_id", domainID, "error", err)
		return 0
	}
	type conceptInfo struct{ Name, Boundary string }
	idToConcept := make(map[string]conceptInfo, len(conceptRefs))
	for _, line := range conceptRefs {
		parts := strings.Split(line, "\t")
		if len(parts) >= 4 {
			idToConcept[parts[0]] = conceptInfo{Name: parts[1], Boundary: parts[3]}
		}
	}

	touched := 0
	for key, points := range groups {
		concept, ok := idToConcept[key.ConceptID]
		if !ok || len(points) == 0 {
			continue
		}

		var text strings.Builder
		for _, p := range points {
			ts := titleSummaryBySource[p.SourceID]
			fmt.Fprintf(&text, "%s\t%s\t%s\t%s\n", unitCenterMap[p.UnitID], ts[0], ts[1], p.Content)
		}

		vars := map[string]string{
			"concept_name":     concept.Name,
			"concept_boundary": concept.Boundary,
			"points":           text.String(),
		}
		data, err := s.llmClient.CompleteJSON(ctx, "kpn_fact_group_entity.md", vars, "extraction")
		if err != nil {
			slog.Warn("unit: kpn fact group entity llm call failed", "domain_id", domainID, "concept_id", key.ConceptID, "source_id", key.SourceID, "error", err)
			continue
		}
		var out factGroupEntityOutput
		if err := json.Unmarshal(data, &out); err != nil {
			slog.Warn("unit: kpn fact group entity parse failed", "domain_id", domainID, "concept_id", key.ConceptID, "source_id", key.SourceID, "error", err)
			continue
		}
		entity := strings.TrimSpace(out.Entity)
		if entity == "" {
			continue // no coherent shared entity — leave these points orphan
		}

		name := joinEntityConcept(entity, concept.Name)
		description := strings.TrimSpace(out.Description)
		boundary := strings.TrimSpace(out.Boundary)
		pointIDs := make([]string, len(points))
		for i, p := range points {
			pointIDs[i] = p.PointID
		}
		if _, err := s.conceptNotifier.ProposeAddCandidate(domainID, name, description, boundary, "fact", entity, pointIDs, key.SourceID, key.ConceptID); err != nil {
			slog.Warn("unit: kpn fact group candidate write failed", "source_id", key.SourceID, "suggested_name", name, "error", err)
			continue
		}
		touched++
	}
	return touched
}

// joinEntityConcept combines an extracted entity with a matched concept name
// into a fact entry's display name (e.g. "MySQL 备份", "达梦数据库备份").
// Deterministic and dedup-aware — this is the ONLY place fact entry names
// get built once a concept match exists (2026-08-05 修订), so the same
// entity+concept pair always yields byte-identical output across calls;
// asking the LLM to phrase this itself let each call produce a slightly
// different fluent string for the same pair, breaking exact-name merging.
// If concept is already a substring of entity (e.g. entity="立项审批流程",
// concept="审批流程"), entity alone already reads complete — return it
// unchanged. Otherwise trims a shared prefix/suffix overlap (e.g.
// entity="培训积分制度", concept="制度目的" share "制度" → "培训积分制度目的",
// not "培训积分制度制度目的"), then decides whether to insert a separating
// space by looking at the JOIN BOUNDARY only — entity's last rune vs.
// concept's first rune after overlap-trimming (2026-08-05 三次修订, replacing
// a whole-string "is entity/concept CJK-only" check): an entity like "Oracle
// RAC 数据库" is Latin-mixed overall but ends in a Han character, so joining
// it with a Han-starting concept like "部署" must not insert a space
// ("Oracle RAC 数据库部署", not "Oracle RAC 数据库 部署") — the old check
// looked at "Oracle"/"RAC" earlier in the string and added a space anyway
// even though the actual join point is entirely Chinese. No space when both
// boundary runes are Han; a single space otherwise (covers a Latin/digit
// rune on either side of the join, e.g. entity="MySQL" + concept="备份").
func joinEntityConcept(entity, concept string) string {
	entity = strings.TrimSpace(entity)
	concept = strings.TrimSpace(concept)
	if concept == "" {
		return entity
	}
	if strings.Contains(entity, concept) {
		return entity
	}

	entityRunes := []rune(entity)
	conceptRunes := []rune(concept)
	maxOverlap := len(entityRunes)
	if len(conceptRunes) < maxOverlap {
		maxOverlap = len(conceptRunes)
	}
	// Minimum overlap of 2 runes — a single coincidentally-shared CJK
	// character (e.g. entity "...系统内核参数" ends in "数", concept
	// "数据库配置" starts with "数") is not a real word overlap and trimming
	// on it mangles the result ("Linux 系统内核参数 据库配置" — missing 数).
	for k := maxOverlap; k >= 2; k-- {
		if string(entityRunes[len(entityRunes)-k:]) == string(conceptRunes[:k]) {
			concept = string(conceptRunes[k:])
			break
		}
	}
	if concept == "" {
		return entity
	}

	conceptRunes = []rune(concept)
	lastEntityRune := entityRunes[len(entityRunes)-1]
	firstConceptRune := conceptRunes[0]
	if unicode.Is(unicode.Han, lastEntityRune) && unicode.Is(unicode.Han, firstConceptRune) {
		return entity + concept
	}
	return entity + " " + concept
}
