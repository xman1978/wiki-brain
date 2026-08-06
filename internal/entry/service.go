package entry

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/foundation/text"
	"github.com/jxman78/wiki-brain/internal/wiki"
)

// Config mirrors config.yml's study 节 concept_* thresholds
// (docs/impl/v1/concept-evolution.md 配置项). Trace only needs
// entry_null_ratio_min (consumed there directly); the rest live here.
type Config struct {
	AddEventMin       int
	AddDistinctMin    int
	AddOverlapMin     float64
	MergeCooccurMin   int
	MergeOverlapMin   float64
	CandidateIdleDays int
	EventWindowDays   int
}

// KPNRematchNotifier lets the KPN cross-Source matching pipeline
// (internal/unit) re-run matching for a kind=add candidate's point_ids
// immediately after confirm gives them a entry_id — before confirm they
// may have sat with zero cross-Source relations (docs/impl/v1/kpn.md 步骤 6).
// SetKPNRematchNotifier no-ops when unset. Returns the created relation_ids
// so the caller can record them on the candidate row — restoring an applied
// candidate cleans up exactly these relations, not anything a later,
// unrelated Source import creates once these points have a entry_id.
type KPNRematchNotifier interface {
	RematchPoints(conceptID string, pointIDs []string) []string
	// ProposeEntriesForDomainOrphans clusters and names domainID's standing
	// entry_id-empty KPs and writes each cluster as a pending_confirm
	// add candidate — the 知识领域页面"+ 新增概念"按钮's on-demand entry
	// point into the same content_driven proposal pipeline KPN's
	// per-Source pass otherwise drives automatically (docs/impl/v1/kpn.md
	// 步骤 3). Returns how many proposals were written.
	ProposeEntriesForDomainOrphans(ctx context.Context, domainID string) (int, error)
}

type Service struct {
	store       *Store
	cfg         Config
	wikiSvc     *wiki.Service      // optional: nil-safe, needs_recompile flagging no-ops without it
	kpnNotifier KPNRematchNotifier // optional: nil-safe, rematch no-ops without it
}

func NewService(store *Store, cfg Config, wikiSvc *wiki.Service) *Service {
	return &Service{store: store, cfg: cfg, wikiSvc: wikiSvc}
}

func (s *Service) SetKPNRematchNotifier(n KPNRematchNotifier) {
	s.kpnNotifier = n
}

// ProposeAddCandidate is KPN cross-Source matching's entry point
// (docs/impl/v1/kpn.md 步骤 3) for entry_id-empty KPs: merges into an
// existing pending content_driven candidate for the same domain AND same
// suggested_name when one exists (matched by evidence.origin, so it never
// merges into a usage_driven candidate that happens to share a domain; the
// suggested_name check keeps distinct clusters proposed by the same
// kpn_entry_propose.md call — or by different calls — from collapsing
// into one candidate just because they share a domain), otherwise creates a
// new one. Unlike scanAddClusters this has no event/question/overlap
// threshold — KPN calls it once per cluster per import and it always fires,
// so a document's orphan KPs never sit unprocessed indefinitely.
func (s *Service) ProposeAddCandidate(domainID, suggestedName, suggestedDescription, suggestedBoundary, conceptKind, entity string, pointIDs []string, sourceID string) (candidateID string, err error) {
	if domainID == "" || len(pointIDs) == 0 {
		return "", fmt.Errorf("concept: propose add candidate requires domain_id and point_ids")
	}
	suggestedName = strings.TrimSpace(suggestedName)
	suggestedDescription = strings.TrimSpace(suggestedDescription)
	suggestedBoundary = strings.TrimSpace(suggestedBoundary)
	entity = strings.TrimSpace(entity)
	conceptKind, err = ValidateEntryKind(conceptKind)
	if err != nil {
		return "", err
	}

	pending, err := s.store.ListCandidatesByKindStatus(KindAdd, StatusPendingConfirm)
	if err != nil {
		return "", err
	}
	for _, p := range pending {
		if !p.DomainID.Valid || p.DomainID.String != domainID || !p.SuggestedName.Valid || p.SuggestedName.String != suggestedName {
			continue
		}
		var existing ContentDrivenEvidence
		if err := json.Unmarshal([]byte(p.Evidence), &existing); err != nil || existing.Origin != "content_driven" {
			continue
		}
		evidence := ContentDrivenEvidence{
			Origin:      "content_driven",
			SourceIDs:   dedupStrings(append(existing.SourceIDs, sourceID)),
			Description: existing.Description, // keep original, don't drift on each new batch
			Boundary:    existing.Boundary,     // same — don't drift on each new batch
			Entity:      existing.Entity,       // first-seen wins, tracked for alias detection below
			Aliases:     existing.Aliases,
		}
		if evidence.Description == "" {
			evidence.Description = suggestedDescription
		}
		if evidence.Boundary == "" {
			evidence.Boundary = suggestedBoundary
		}
		if evidence.Entity == "" {
			evidence.Entity = entity
		} else if entity != "" && !strings.EqualFold(entity, evidence.Entity) {
			// This batch names the same real-world thing under a different
			// string than the entity already recorded on this pending
			// candidate — record it as an alias instead of dropping it
			// (docs/impl/v1/kpn.md 步骤 3, 2026-08-05).
			alreadyKnown := false
			for _, a := range evidence.Aliases {
				if strings.EqualFold(a, entity) {
					alreadyKnown = true
					break
				}
			}
			if !alreadyKnown {
				evidence.Aliases = append(evidence.Aliases, entity)
			}
		}
		if err := s.store.MergeAddCandidatePoints(p.CandidateID, pointIDs, evidence); err != nil {
			return "", err
		}
		return p.CandidateID, nil
	}

	evidence := ContentDrivenEvidence{Origin: "content_driven", SourceIDs: []string{sourceID}, Description: suggestedDescription, Boundary: suggestedBoundary, Entity: entity}
	reason := fmt.Sprintf("跨 Source KPN 匹配发现无概念归属的 KP 聚类：建议概念「%s」", suggestedName)
	candidateID, err = s.store.InsertAddCandidate(sql.NullString{String: domainID, Valid: true}, suggestedName, conceptKind, pointIDs, nil, evidence, reason)
	if err != nil {
		return "", err
	}
	return candidateID, nil
}

// CreateManualCandidate implements the "新增" button in the concept
// evolution UI: the button itself only opens a client-side draft form (no
// server call, nothing persisted) — this is what actually runs when the
// user clicks that draft's own "确认" (save as pending_confirm) or "驳回"
// (save then immediately reject) button, with whatever domain/name/point_ids
// they'd filled in by then. Either way it inserts a kind=add candidate
// exactly like any other; the concept itself is still only ever created
// later through the normal Confirm path when that saved candidate is
// opened from the 待确认 list — evidence.origin="manual" is audit-only, no
// design doc defines a bypass of the human-confirm step.
func (s *Service) CreateManualCandidate(domainID, suggestedName, description, conceptKind string, pointIDs []string) (candidateID string, err error) {
	conceptKind, err = ValidateEntryKind(conceptKind)
	if err != nil {
		return "", err
	}
	var domain sql.NullString
	if domainID != "" {
		domain = sql.NullString{String: domainID, Valid: true}
	}
	if pointIDs == nil {
		pointIDs = []string{}
	}
	// Description round-trips through evidence.description like a
	// content_driven candidate's does — the confirm dialog prefills its
	// editable description field from there whichever origin, so typing one
	// on the draft form survives the save-then-reopen-to-confirm gap.
	evidence := ContentDrivenEvidence{Origin: "manual", Description: description}
	return s.store.InsertAddCandidate(domain, suggestedName, conceptKind, pointIDs, nil, evidence, "人工手动新增概念候选")
}

// Scan runs the two clustering subtasks plus idle expiry
// (docs/impl/v1/concept-evolution.md 步骤 2), appended to Study's task chain
// after its own step 6. Each subtask logs and continues on error, matching
// study.md's "单步异常记录 error 日志，不中断本轮后续步骤".
func (s *Service) Scan() ScanSummary {
	var summary ScanSummary

	if err := s.scanAddClusters(&summary); err != nil {
		slog.Error("concept: scan add clusters failed", "error", err)
	}
	if err := s.scanMergeCandidates(&summary); err != nil {
		slog.Error("concept: scan merge candidates failed", "error", err)
	}
	expired, err := s.store.ExpireIdleCandidates(s.cfg.CandidateIdleDays)
	if err != nil {
		slog.Error("concept: expire idle candidates failed", "error", err)
	} else {
		summary.Expired = len(expired)
	}

	return summary
}

// addCluster is a greedily-grown group of entry_gap events whose point-id
// sets mutually overlap at least AddOverlapMin against the cluster's running
// union (docs/impl/v1/concept-evolution.md 步骤 2 新增聚类).
type addCluster struct {
	events   []GapPointEvent
	unionSet map[string]bool
}

func clusterGapEvents(events []GapPointEvent, overlapMin float64) []*addCluster {
	var clusters []*addCluster
	for _, e := range events {
		eSet := toSet(e.PointIDs)
		var target *addCluster
		for _, c := range clusters {
			if jaccard(eSet, c.unionSet) >= overlapMin {
				target = c
				break
			}
		}
		if target == nil {
			target = &addCluster{unionSet: map[string]bool{}}
			clusters = append(clusters, target)
		}
		target.events = append(target.events, e)
		for pid := range eSet {
			target.unionSet[pid] = true
		}
	}
	return clusters
}

// clusterOverlap reports the average member-vs-final-union Jaccard, purely
// as reportable evidence — cluster membership itself already enforced
// overlapMin at join time.
func clusterOverlap(c *addCluster) float64 {
	if len(c.events) == 0 {
		return 0
	}
	total := 0.0
	for _, e := range c.events {
		total += jaccard(toSet(e.PointIDs), c.unionSet)
	}
	return total / float64(len(c.events))
}

func toSet(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func setToSortedSlice(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func dedupStrings(ss []string) []string {
	seen := make(map[string]bool, len(ss))
	var out []string
	for _, v := range ss {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// scanAddClusters implements docs/impl/v1/concept-evolution.md 步骤 2 新增聚类.
func (s *Service) scanAddClusters(summary *ScanSummary) error {
	events, err := s.store.FetchEntryGapEvents(s.cfg.EventWindowDays)
	if err != nil {
		return err
	}
	summary.EntryGapEventCount = len(events)
	if len(events) == 0 {
		return nil
	}

	seen, err := s.store.SeenAddEventIDs()
	if err != nil {
		return err
	}

	var fresh []GapPointEvent
	for _, e := range events {
		if seen[e.EventID] || len(e.PointIDs) == 0 {
			continue
		}
		fresh = append(fresh, e)
	}
	if len(fresh) == 0 {
		return nil
	}

	clusters := clusterGapEvents(fresh, s.cfg.AddOverlapMin)

	pending, err := s.store.ListCandidatesByKindStatus(KindAdd, StatusPendingConfirm)
	if err != nil {
		return err
	}

	for _, c := range clusters {
		distinctQ := make(map[string]bool)
		var eventIDs []string
		for _, e := range c.events {
			eventIDs = append(eventIDs, e.EventID)
			if e.QuestionHash != "" {
				distinctQ[e.QuestionHash] = true
			}
		}
		evidence := AddEvidence{EventCount: len(c.events), DistinctCount: len(distinctQ), Overlap: clusterOverlap(c)}

		// A cluster overlapping an already-pending candidate is new signal
		// for it, regardless of whether this cluster alone meets threshold
		// (docs/impl/v1/concept-evolution.md 步骤 2: "同簇已有 pending_confirm
		// 候选...更新...不重复建行").
		matched := false
		for _, p := range pending {
			var existingPoints []string
			if err := json.Unmarshal([]byte(p.PointIDs), &existingPoints); err != nil {
				continue
			}
			if jaccard(toSet(existingPoints), c.unionSet) >= s.cfg.AddOverlapMin {
				if err := s.store.UpdateCandidateSignal(p.CandidateID, evidence, eventIDs); err != nil {
					return err
				}
				summary.AddUpdated++
				matched = true
				break
			}
		}
		if matched {
			continue
		}

		if len(c.events) < s.cfg.AddEventMin || len(distinctQ) < s.cfg.AddDistinctMin {
			continue
		}

		pointIDs := setToSortedSlice(c.unionSet)
		domainID, name, err := s.deriveAddSuggestion(pointIDs)
		if err != nil {
			slog.Error("concept: derive add suggestion failed", "error", err)
			continue
		}

		reason := fmt.Sprintf("概念缺口聚类：事件数=%d，不同问题数=%d，重叠度=%.2f", len(c.events), len(distinctQ), evidence.Overlap)
		// Usage-driven clusters (docs/impl/v1/concept-evolution.md 步骤 2) have
		// no LLM kind classification of their own — default to concept, same
		// as the DB column default; kind:fact here would require asserting a
		// judgment this pipeline never makes, unlike kpn_entry_propose.md's
		// content-driven clusters.
		if _, err := s.store.InsertAddCandidate(domainID, name, EntryKindConcept, pointIDs, eventIDs, evidence, reason); err != nil {
			return err
		}
		summary.AddCreated++
	}
	return nil
}

// deriveAddSuggestion computes a cluster's suggested domain (majority vote
// over the cluster KUs' source domain_id) and suggested name (high-frequency
// terms from the KU centers), per docs/impl/v1/concept-evolution.md 步骤 2.
func (s *Service) deriveAddSuggestion(pointIDs []string) (sql.NullString, string, error) {
	kuInfos, err := s.store.KUInfoForPoints(pointIDs)
	if err != nil {
		return sql.NullString{}, "", err
	}

	seenUnit := make(map[string]bool)
	var sourceIDs, centers []string
	for _, info := range kuInfos {
		if seenUnit[info.UnitID] {
			continue
		}
		seenUnit[info.UnitID] = true
		sourceIDs = append(sourceIDs, info.SourceID)
		centers = append(centers, info.Center)
	}

	domains, err := s.store.SourceDomains(dedupStrings(sourceIDs))
	if err != nil {
		return sql.NullString{}, "", err
	}

	counts := make(map[string]int)
	var order []string
	for _, sid := range sourceIDs {
		d := domains[sid]
		if !d.Valid || d.String == "" {
			continue
		}
		if counts[d.String] == 0 {
			order = append(order, d.String)
		}
		counts[d.String]++
	}
	var domainID sql.NullString
	best := 0
	for _, k := range order {
		if counts[k] > best {
			best = counts[k]
			domainID = sql.NullString{String: k, Valid: true}
		}
	}

	return domainID, suggestName(centers), nil
}

// suggestName tokenizes every KU center, drops stop words, and joins the top
// 3 terms by frequency — a program-computed suggestion, no LLM call
// (docs/impl/v1/concept-evolution.md 步骤 2, human may rename at confirm time).
func suggestName(centers []string) string {
	freq := make(map[string]int)
	var order []string
	for _, c := range centers {
		for _, tok := range text.Tokenize(text.Normalize(c)) {
			if tok == "" || text.StopWords[tok] {
				continue
			}
			if freq[tok] == 0 {
				order = append(order, tok)
			}
			freq[tok]++
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return freq[order[i]] > freq[order[j]] })
	if len(order) > 3 {
		order = order[:3]
	}
	return strings.Join(order, "、")
}

// pairKey is an unordered entry_id pair, canonicalized A<B for map keys.
type pairKey struct{ A, B string }

type pairAgg struct {
	cooccur int
	pointsA map[string]bool
	pointsB map[string]bool
}

// scanMergeCandidates implements docs/impl/v1/concept-evolution.md 步骤 2 合并统计.
func (s *Service) scanMergeCandidates(summary *ScanSummary) error {
	traces, err := s.store.TracesInWindow(s.cfg.EventWindowDays)
	if err != nil {
		return err
	}
	if len(traces) == 0 {
		return nil
	}

	allPoints := make(map[string]bool)
	for _, t := range traces {
		for _, p := range t.PointIDs {
			allPoints[p] = true
		}
	}
	pointEntry, err := s.store.PointEntryMap(setToSortedSlice(allPoints))
	if err != nil {
		return err
	}
	if len(pointEntry) == 0 {
		return nil
	}

	agg := make(map[pairKey]*pairAgg)
	totalTraces := make(map[string]int) // entry_id -> # traces where it appears at all (any point)
	for _, t := range traces {
		conceptPoints := make(map[string]map[string]bool)
		for _, p := range t.PointIDs {
			cid, ok := pointEntry[p]
			if !ok {
				continue
			}
			if conceptPoints[cid] == nil {
				conceptPoints[cid] = make(map[string]bool)
			}
			conceptPoints[cid][p] = true
		}
		for cid := range conceptPoints {
			totalTraces[cid]++
		}
		if len(conceptPoints) < 2 {
			continue
		}

		var entries []string
		for c := range conceptPoints {
			entries = append(entries, c)
		}
		sort.Strings(entries)

		for i := 0; i < len(entries); i++ {
			for j := i + 1; j < len(entries); j++ {
				key := pairKey{A: entries[i], B: entries[j]}
				a := agg[key]
				if a == nil {
					a = &pairAgg{pointsA: make(map[string]bool), pointsB: make(map[string]bool)}
					agg[key] = a
				}
				a.cooccur++
				for p := range conceptPoints[entries[i]] {
					a.pointsA[p] = true
				}
				for p := range conceptPoints[entries[j]] {
					a.pointsB[p] = true
				}
			}
		}
	}

	pendingMerges, err := s.store.ListCandidatesByKindStatus(KindMerge, StatusPendingConfirm)
	if err != nil {
		return err
	}

	for key, a := range agg {
		if a.cooccur < s.cfg.MergeCooccurMin {
			continue
		}
		totalA, totalB := totalTraces[key.A], totalTraces[key.B]
		union := totalA + totalB - a.cooccur
		overlap := 0.0
		if union > 0 {
			overlap = float64(a.cooccur) / float64(union)
		}
		if overlap < s.cfg.MergeOverlapMin {
			continue
		}

		mergeFrom := []string{key.A, key.B}
		evidence := MergeEvidence{
			CooccurCount: a.cooccur,
			OverlapRatio: overlap,
			TotalTracesA: totalA,
			TotalTracesB: totalB,
			PointIDsA:    setToSortedSlice(a.pointsA),
			PointIDsB:    setToSortedSlice(a.pointsB),
		}

		var existing *CandidateRow
		for i := range pendingMerges {
			var mf []string
			if err := json.Unmarshal([]byte(pendingMerges[i].MergeFrom), &mf); err != nil {
				continue
			}
			if samePair(mf, mergeFrom) {
				existing = &pendingMerges[i]
				break
			}
		}

		if existing != nil {
			if err := s.store.UpdateMergeCandidateSignal(existing.CandidateID, evidence); err != nil {
				return err
			}
			summary.MergeUpdated++
			continue
		}

		pointIDs := dedupStrings(append(setToSortedSlice(a.pointsA), setToSortedSlice(a.pointsB)...))
		reason := fmt.Sprintf("概念对共现：共同采用次数=%d，KP 重叠比例=%.2f", a.cooccur, overlap)
		if _, err := s.store.InsertMergeCandidate(mergeFrom, pointIDs, evidence, reason); err != nil {
			return err
		}
		summary.MergeCreated++
	}
	return nil
}

func samePair(a, b []string) bool {
	if len(a) != 2 || len(b) != 2 {
		return false
	}
	sa := append([]string{}, a...)
	sb := append([]string{}, b...)
	sort.Strings(sa)
	sort.Strings(sb)
	return sa[0] == sb[0] && sa[1] == sb[1]
}

// Confirm implements POST /entries/candidates/:id/confirm
// (docs/impl/v1/concept-evolution.md 步骤 3): dispatches to the kind-specific
// execution, each running in its own single transaction. There is no auto
// mode — every confirm here is a human action.
func (s *Service) Confirm(candidateID string, addReq *ConfirmAddRequest, mergeReq *ConfirmMergeRequest) (*ConfirmResult, error) {
	c, err := s.store.GetCandidate(candidateID)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, fmt.Errorf("concept: candidate not found: %s", candidateID)
	}
	if c.Status != StatusPendingConfirm {
		return nil, fmt.Errorf("concept: confirm only valid for pending_confirm candidates, %s is %s", candidateID, c.Status)
	}

	switch c.Kind {
	case KindAdd:
		return s.confirmAdd(c, addReq)
	case KindMerge:
		return s.confirmMerge(c, mergeReq)
	default:
		return nil, fmt.Errorf("concept: unknown candidate kind %q", c.Kind)
	}
}

func (s *Service) confirmAdd(c *CandidateRow, req *ConfirmAddRequest) (*ConfirmResult, error) {
	var pointIDs []string
	if err := json.Unmarshal([]byte(c.PointIDs), &pointIDs); err != nil {
		return nil, fmt.Errorf("concept: confirm add: unmarshal point_ids: %w", err)
	}
	// The confirm dialog's KP picker may add/remove points from the
	// candidate's original suggestion — when present, PointIDs replaces it
	// wholesale (not a delta). Migration still only touches entry_id-NULL
	// KUs (store layer), so a stale/already-claimed id is silently skipped.
	if req != nil && req.PointIDs != nil {
		pointIDs = req.PointIDs
	}
	if len(pointIDs) == 0 {
		return nil, fmt.Errorf("concept: confirm add requires at least one point_id")
	}

	// "归入已有概念" (docs/impl/v1/kpn.md 步骤 6): skip creating a concept,
	// assign point_ids to req.EntryID directly. Mutually exclusive with
	// the new-concept fields below.
	if req != nil && req.EntryID != "" {
		active, err := s.store.ConceptActive(req.EntryID)
		if err != nil {
			return nil, err
		}
		if !active {
			return nil, fmt.Errorf("concept: confirm add: entry_id %q does not exist or has been merged away", req.EntryID)
		}
		reason := fmt.Sprintf("人工确认归入已有概念：%s", req.EntryID)
		migrated, err := s.store.ConfirmAssign(c.CandidateID, req.EntryID, pointIDs, reason)
		if err != nil {
			return nil, err
		}
		if s.kpnNotifier != nil {
			relationIDs := s.kpnNotifier.RematchPoints(req.EntryID, pointIDs)
			s.recordKPNRelationIDs(c.CandidateID, relationIDs)
		}
		return &ConfirmResult{Candidate: *c, EntryID: req.EntryID, MigratedKUs: migrated}, nil
	}

	name := c.SuggestedName.String
	domainID := ""
	if c.DomainID.Valid {
		domainID = c.DomainID.String
	}
	description := ""
	conceptKind := c.EntryKind
	if req != nil {
		if req.SuggestedName != "" {
			name = req.SuggestedName
		}
		if req.DomainID != "" {
			domainID = req.DomainID
		}
		description = req.Description
		if req.EntryKind != "" {
			conceptKind = req.EntryKind
		}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("concept: confirm add requires a suggested_name")
	}
	if domainID == "" {
		return nil, fmt.Errorf("concept: confirm add requires domain_id (candidate has none)")
	}
	conceptKind, err := ValidateEntryKind(conceptKind)
	if err != nil {
		return nil, err
	}

	// boundary/aliases come from the candidate's own evidence (kpn_entry_
	// propose.md's suggested_boundary, and entity aliases accumulated across
	// merges in ProposeAddCandidate, 2026-08-05) — unlike name/description,
	// there's no confirm-dialog override for these yet, so they're always
	// whatever the LLM/merge pipeline produced.
	var evidence ContentDrivenEvidence
	_ = json.Unmarshal([]byte(c.Evidence), &evidence)
	if req != nil && req.Boundary != "" {
		evidence.Boundary = req.Boundary
	}

	conceptID := uuid.New().String()
	reason := fmt.Sprintf("人工确认新增概念：%s（领域 %s）", name, domainID)
	migrated, err := s.store.ConfirmAdd(c.CandidateID, conceptID, domainID, name, description, evidence.Boundary, conceptKind, evidence.Aliases, pointIDs, reason)
	if err != nil {
		return nil, err
	}
	if s.kpnNotifier != nil {
		relationIDs := s.kpnNotifier.RematchPoints(conceptID, pointIDs)
		s.recordKPNRelationIDs(c.CandidateID, relationIDs)
	}

	return &ConfirmResult{Candidate: *c, EntryID: conceptID, MigratedKUs: migrated}, nil
}

// recordKPNRelationIDs persists the relation_ids RematchPoints just created
// onto the candidate row, so a later restore can clean up precisely those
// relations. Best-effort like RematchPoints itself: the confirm already
// committed, a failure here only means a future restore leaves these
// relations behind rather than blocking anything now.
func (s *Service) recordKPNRelationIDs(candidateID string, relationIDs []string) {
	if len(relationIDs) == 0 {
		return
	}
	if err := s.store.SetCandidateKPNRelationIDs(candidateID, relationIDs); err != nil {
		slog.Warn("concept: record kpn relation ids failed", "candidate_id", candidateID, "error", err)
	}
}

func (s *Service) confirmMerge(c *CandidateRow, req *ConfirmMergeRequest) (*ConfirmResult, error) {
	var mergeFrom []string
	if err := json.Unmarshal([]byte(c.MergeFrom), &mergeFrom); err != nil {
		return nil, fmt.Errorf("concept: confirm merge: unmarshal merge_from: %w", err)
	}
	if req == nil || req.Target == "" {
		return nil, fmt.Errorf("concept: confirm merge requires target")
	}
	validTarget := false
	for _, m := range mergeFrom {
		if m == req.Target {
			validTarget = true
			break
		}
	}
	if !validTarget {
		return nil, fmt.Errorf("concept: target %q not in merge_from %v", req.Target, mergeFrom)
	}

	reason := fmt.Sprintf("人工确认合并概念：%v -> %s", mergeFrom, req.Target)
	migrated, _, err := s.store.ConfirmMerge(c.CandidateID, mergeFrom, req.Target, reason)
	if err != nil {
		return nil, err
	}

	flagged := s.flagMergedEntryPages(mergeFrom, reason)

	return &ConfirmResult{Candidate: *c, MigratedKUs: migrated, FlaggedPages: flagged}, nil
}

// flagMergedEntryPages marks needs_recompile on the active Wiki page for
// every concept involved in the merge — both the target (its qualifying KP
// set changed) and every concept merged away (no longer a valid entry point)
// — via the Wiki module's own interface, post-commit
// (docs/impl/v1/concept-evolution.md 步骤 3: "调 Wiki 模块接口标记
// needs_recompile（不自动重编译）").
func (s *Service) flagMergedEntryPages(mergeFrom []string, reason string) int {
	if s.wikiSvc == nil {
		return 0
	}
	flagged := 0
	for _, cid := range mergeFrom {
		page, err := s.wikiSvc.GetActivePageByEntryID(cid)
		if err != nil {
			slog.Error("concept: get active page by concept failed", "entry_id", cid, "error", err)
			continue
		}
		if page == nil {
			continue
		}
		if err := s.wikiSvc.MarkNeedsRecompile(page.PageID, reason); err != nil {
			slog.Error("concept: mark needs_recompile failed", "page_id", page.PageID, "error", err)
			continue
		}
		flagged++
	}
	return flagged
}

// Reject implements POST /entries/candidates/:id/reject: no structural
// change, candidate + learning_result both move to rejected.
func (s *Service) Reject(candidateID string) error {
	c, err := s.store.GetCandidate(candidateID)
	if err != nil {
		return err
	}
	if c == nil {
		return fmt.Errorf("concept: candidate not found: %s", candidateID)
	}
	if c.Status != StatusPendingConfirm {
		return fmt.Errorf("concept: reject only valid for pending_confirm candidates, %s is %s", candidateID, c.Status)
	}
	return s.store.Reject(candidateID)
}

// Delete implements DELETE /entries/candidates/:id: hard-removes a
// pending_confirm candidate (and its learning_result) — for discarding a
// candidate entirely rather than keeping an audit trail in 已驳回 (that's
// what Reject is for). Only pending_confirm candidates qualify: applied
// candidates have already created real concept/KU structure (Restore is the
// path for those), and rejected/expired ones are already out of the active
// list.
func (s *Service) Delete(candidateID string) error {
	c, err := s.store.GetCandidate(candidateID)
	if err != nil {
		return err
	}
	if c == nil {
		return fmt.Errorf("concept: candidate not found: %s", candidateID)
	}
	if c.Status != StatusPendingConfirm {
		return fmt.Errorf("concept: delete only valid for pending_confirm candidates, %s is %s", candidateID, c.Status)
	}
	return s.store.DeleteCandidate(candidateID)
}

// Restore implements POST /entries/candidates/:id/restore: moves a
// rejected or applied candidate back to pending_confirm. Rejected candidates
// restore unconditionally (reject never mutated data). Applied candidates
// only restore when this candidate created a brand-new concept (kind=add,
// CreatedNewEntry) — assign-to-existing and merge confirms touch a concept
// this candidate didn't create, so undoing them isn't this candidate's to
// do (no design doc defines that scope; agreed with the user to bound
// restore this way for now).
func (s *Service) Restore(candidateID string) error {
	c, err := s.store.GetCandidate(candidateID)
	if err != nil {
		return err
	}
	if c == nil {
		return fmt.Errorf("concept: candidate not found: %s", candidateID)
	}

	switch c.Status {
	case StatusRejected:
		return s.store.RestoreRejected(candidateID)
	case StatusApplied:
		if c.Kind != KindAdd || !c.CreatedNewEntry || !c.ResolvedEntryID.Valid {
			return fmt.Errorf("concept: restore not supported for this applied candidate (only new-concept kind=add confirms are restorable)")
		}
		conceptID := c.ResolvedEntryID.String
		if s.wikiSvc != nil {
			page, err := s.wikiSvc.GetActivePageByEntryID(conceptID)
			if err != nil {
				return fmt.Errorf("concept: restore: check wiki page: %w", err)
			}
			if page != nil {
				return fmt.Errorf("concept: restore: concept %s still has an active wiki page (%s) — archive or reassign it first", conceptID, page.PageID)
			}
		}
		var pointIDs []string
		if err := json.Unmarshal([]byte(c.PointIDs), &pointIDs); err != nil {
			return fmt.Errorf("concept: restore: unmarshal point_ids: %w", err)
		}
		var relationIDs []string
		if err := json.Unmarshal([]byte(c.KPNRelationIDs), &relationIDs); err != nil {
			return fmt.Errorf("concept: restore: unmarshal kpn_relation_ids: %w", err)
		}
		reason := fmt.Sprintf("人工从已执行恢复至待确认：撤销新建概念 %s", conceptID)
		return s.store.RestoreAppliedNewEntry(candidateID, conceptID, pointIDs, relationIDs, reason)
	default:
		return fmt.Errorf("concept: restore only valid for applied/rejected candidates, %s is %s", candidateID, c.Status)
	}
}

func (s *Service) ListCandidates(status string) ([]CandidateRow, error) {
	return s.store.ListCandidates(status)
}

// ListDomainCandidateViews is GET /entries/candidates/by-domain's data
// source — the 知识领域 page's merged concept grid folds these (pending/
// rejected/expired kind=add candidates) in alongside real entries, in place
// of the old separate status-tabbed list.
func (s *Service) ListDomainCandidateViews(domainID string) ([]CandidateView, error) {
	rows, err := s.store.ListDomainAddCandidates(domainID)
	if err != nil {
		return nil, err
	}
	views := make([]CandidateView, len(rows))
	for i, r := range rows {
		views[i] = toView(r)
	}
	return views, nil
}

func (s *Service) GetEntryDetail(conceptID string) (*EntryDetail, error) {
	return s.store.GetEntryDetail(conceptID)
}

// UpdateEntryMeta mirrors name/description editing: empty kind means "not
// touched by this edit" and keeps the concept's current kind (the edit
// modal's existing name/description fields don't currently surface kind —
// docs/impl/v1/kpn.md 步骤 3 makes kind editable but doesn't require every
// caller to resend it), a non-empty value must be a valid concept/fact
// classification.
func (s *Service) UpdateEntryMeta(conceptID, name, description, kind string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("concept: name is required")
	}
	if kind == "" {
		detail, err := s.store.GetEntryDetail(conceptID)
		if err != nil {
			return err
		}
		if detail == nil {
			return fmt.Errorf("concept: concept %s not found or merged away", conceptID)
		}
		kind = detail.Kind
	} else {
		var err error
		kind, err = ValidateEntryKind(kind)
		if err != nil {
			return err
		}
	}
	return s.store.UpdateEntryMeta(conceptID, name, strings.TrimSpace(description), kind)
}

func (s *Service) AddEntryPoints(conceptID string, pointIDs []string) (int, error) {
	return s.store.AddEntryPoints(conceptID, pointIDs)
}

func (s *Service) RemoveEntryPoint(conceptID, pointID string) (int, error) {
	return s.store.RemoveEntryPoint(conceptID, pointID)
}

func (s *Service) GetCandidate(candidateID string) (*CandidateRow, error) {
	return s.store.GetCandidate(candidateID)
}

// ListActiveEntries is GET /entries's data source — populates the confirm
// UI's domain/concept pickers (docs/impl/v1/kpn.md 步骤 6), not consumed by
// any matching or confirm logic.
func (s *Service) ListActiveEntries(domainID string) ([]EntryInfo, error) {
	return s.store.ListActiveEntries(domainID)
}

// ListActiveEntryReferences satisfies unit.EntryNotifier — gives
// kpn_entry_propose.md (docs/impl/v1/kpn.md 步骤 3) the domain's existing
// concepts as a granularity/abstraction-level reference. Previously this
// only handed over bare names; description and boundary are exactly the
// curation signal preset entries were authored with to pin down abstraction
// level and scope, so they're formatted in too — one line per entry, kept
// as plain strings (not a shared struct) so entry keeps not importing unit
// and vice versa (see unit.EntryNotifier's doc comment on the intentional
// one-directional wiring).
func (s *Service) ListActiveEntryReferences(domainID string) ([]string, error) {
	infos, err := s.store.ListActiveEntries(domainID)
	if err != nil {
		return nil, err
	}
	lines := make([]string, 0, len(infos))
	for _, c := range infos {
		line := c.Name
		if c.Description != "" {
			line += "：" + c.Description
		}
		if c.Boundary != "" {
			line += "｜边界：" + c.Boundary
		}
		lines = append(lines, line)
	}

	// The per-Source KPN pass calls kpn_entry_propose.md once per Source, so
	// without this a second Source whose orphans belong to the same
	// not-yet-confirmed concept has no way to know a first Source already
	// proposed it — it invents its own name, and ProposeAddCandidate's
	// exact-suggested_name merge (service.go 上方) then never fires, splitting
	// one real concept into many pending_confirm candidates (observed after
	// the 2026-08-05 wipe/rematch: ~20 "Oracle RAC*" candidates that should
	// have been a handful). Surfacing pending content_driven add candidates
	// here too, so the prompt can tell the model to reuse the exact name.
	pending, err := s.store.ListCandidatesByKindStatus(KindAdd, StatusPendingConfirm)
	if err != nil {
		slog.Warn("concept: list active entry references: list pending add candidates failed", "domain_id", domainID, "error", err)
		return lines, nil
	}
	for _, p := range pending {
		if !p.DomainID.Valid || p.DomainID.String != domainID || !p.SuggestedName.Valid || p.SuggestedName.String == "" {
			continue
		}
		var ev ContentDrivenEvidence
		if err := json.Unmarshal([]byte(p.Evidence), &ev); err != nil || ev.Origin != "content_driven" {
			continue
		}
		line := p.SuggestedName.String + "（待确认，同名请直接复用，不要改名）"
		if ev.Description != "" {
			line += "：" + ev.Description
		}
		if ev.Boundary != "" {
			line += "｜边界：" + ev.Boundary
		}
		lines = append(lines, line)
	}
	return lines, nil
}

// ListActiveConceptEntryReferences satisfies unit.EntryNotifier — gives
// kpn_fact_concept_match.md (docs/impl/v1/kpn.md 步骤 3 二阶段：fact 簇与
// domain 已有 concept 词条组合命名, 2026-08-05) the domain's existing
// kind=concept entries as a match candidate list — kind=fact entries are
// excluded, a fact cluster combines with a concept, never with another
// fact. One line per entry: "entry_id\tname\tdescription\tboundary",
// tab-separated so the unit package can read entry_id back out without a
// shared struct type, keeping this wiring one-directional like
// ListActiveEntryReferences above.
func (s *Service) ListActiveConceptEntryReferences(domainID string) ([]string, error) {
	infos, err := s.store.ListActiveEntries(domainID)
	if err != nil {
		return nil, err
	}
	lines := make([]string, 0, len(infos))
	for _, c := range infos {
		if c.Kind != EntryKindConcept {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%s", c.EntryID, c.Name, c.Description, c.Boundary))
	}
	return lines, nil
}

// ListPendingAddPointIDs satisfies unit.EntryNotifier — returns point_ids
// already sitting on pending_confirm kind=add candidates in domainID so
// orphan proposal can skip them (docs/impl/v1/kpn.md 步骤 3).
func (s *Service) ListPendingAddPointIDs(domainID string) ([]string, error) {
	if domainID == "" {
		return nil, nil
	}
	pending, err := s.store.ListCandidatesByKindStatus(KindAdd, StatusPendingConfirm)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var out []string
	for _, p := range pending {
		if !p.DomainID.Valid || p.DomainID.String != domainID {
			continue
		}
		var pointIDs []string
		if err := json.Unmarshal([]byte(p.PointIDs), &pointIDs); err != nil {
			continue
		}
		for _, id := range pointIDs {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// ErrNoOrphanClusterer is returned by ProposeEntriesFromDomainOrphans when
// no KPNRematchNotifier has been wired (SetKPNRematchNotifier), which in
// practice never happens outside tests — production wiring always sets it
// to the unit module's Service.
var ErrNoOrphanClusterer = fmt.Errorf("concept: orphan clustering unavailable")

// ProposeEntriesFromDomainOrphans is POST
// /entries/domains/:id/propose-from-orphans's business logic — the 知识领域
// 页面"+ 新增概念"按钮's entry point. Delegates the actual clustering/naming
// LLM call to the unit module (via kpnNotifier, the same notifier that
// already carries RematchPoints) since that's where the KP/KU tables and
// the kpn_entry_propose.md prompt call already live
// (docs/impl/v1/kpn.md 步骤 3) — this method exists so the HTTP handler
// doesn't need to reach into another module's Service directly.
func (s *Service) ProposeEntriesFromDomainOrphans(ctx context.Context, domainID string) (int, error) {
	if domainID == "" {
		return 0, fmt.Errorf("concept: propose entries from domain orphans requires domain_id")
	}
	if s.kpnNotifier == nil {
		return 0, ErrNoOrphanClusterer
	}
	return s.kpnNotifier.ProposeEntriesForDomainOrphans(ctx, domainID)
}

// AvailablePoints is GET /entries/points's data source — populates the
// concept candidate confirm dialog's "add KP" picker.
func (s *Service) AvailablePoints(domainID string) ([]AvailablePointOption, error) {
	return s.store.AvailablePoints(domainID)
}

// ListCandidateViews is GET /entries/candidates and the Study report's
// entry_candidates section's data source (docs/impl/v1/concept-evolution.md
// 步骤 3/5): CandidateRow with its JSON columns parsed for display.
func (s *Service) ListCandidateViews(status string) ([]CandidateView, error) {
	rows, err := s.store.ListCandidates(status)
	if err != nil {
		return nil, err
	}
	views := make([]CandidateView, len(rows))
	for i, r := range rows {
		views[i] = toView(r)
	}
	return views, nil
}

func (s *Service) GetCandidateView(candidateID string) (*CandidateView, error) {
	row, err := s.store.GetCandidate(candidateID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	v := toView(*row)
	return &v, nil
}
