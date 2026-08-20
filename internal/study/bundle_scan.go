package study

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"

	"github.com/jxman78/wiki-brain/internal/activation"
)

// scanActivationBundles implements docs/impl/v1/activation-bundle.md 步骤 4
// 的显影扫描（Study 步骤 5b，2026-08-20 重设计，取代此前按四元组文本聚类簇
// 显影的口径）：Bundle 身份是归一化四元组，不是 point 集合、也不是聚类簇。
// 两步，分别对称于 ActivationLink 的 question_kp_cooccurrence 累积 /
// ScanCandidates：
//  1. 增量累积——每条尚未计入的 confident 多点 trace，归一化其四元组后累加进
//     bundle_trigger_cooccurrence（跟 point_id 级别的 cooccurrence 是同一种
//     簿记，只是键换成归一化四元组，见 study.md「归一化四元组累积」）；
//  2. 门槛判定——对每个越过 create_confidence_min/create_width_max 的
//     (domain, canonical tuple)，创建或刷新对应 Bundle，成员名单/观测条件
//     由 buildBundleObservedConditionsAndMembers 从全部历史多点 confident
//     trace 里按归一化四元组精确匹配重新算出（跟 buildObservedConditions
//     对 Link 的做法一致：每次全量重算，不做增量缓存）。
func (s *Service) scanActivationBundles() error {
	if s.activationSvc == nil {
		return nil
	}
	ctx := context.Background()

	newTraces, err := s.store.NewMultiPointConfidentTraces()
	if err != nil {
		return err
	}
	if len(newTraces) > 0 {
		traceIDs := make([]string, 0, len(newTraces))
		for _, t := range newTraces {
			traceIDs = append(traceIDs, t.TraceID)
			domainIDs, err := s.store.DomainIDsForPoints(t.DirectPointIDs)
			if err != nil {
				slog.Error("study: domain ids for bundle trigger trace failed", "trace_id", t.TraceID, "error", err)
				continue
			}
			subject, intent, audience, constraint := t.Subject, t.Intent, t.Audience, t.Constraint
			if len(domainIDs) > 0 {
				ns, ni, na, nc, err := s.activationSvc.NormalizeTuple(ctx, domainIDs, subject, intent, audience, constraint)
				if err != nil {
					slog.Warn("study: bundle trigger tuple normalization failed, using raw quad", "trace_id", t.TraceID, "error", err)
				} else {
					subject, intent, audience, constraint = ns, ni, na, nc
				}
			}
			domainID := ""
			if len(domainIDs) > 0 {
				domainID = domainIDs[0]
			}
			if err := s.store.UpsertBundleTriggerCooccurrence(domainID, subject, intent, audience, constraint); err != nil {
				slog.Error("study: upsert bundle trigger cooccurrence failed", "trace_id", t.TraceID, "error", err)
			}
		}
		if err := s.store.MarkBundleDedup(traceIDs); err != nil {
			slog.Error("study: mark bundle dedup failed", "error", err)
		}
	}

	candidates, err := s.store.ScanBundleTriggerCandidates(s.cfg.CreateConfidenceMin, s.cfg.CreateWidthMax)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return s.weakenBundlesWithExpiredCoreMembers()
	}

	matcher := s.activationSvc.BundleMatcher()
	for _, c := range candidates {
		conds, members, err := s.buildBundleObservedConditionsAndMembers(ctx, c.Subject, c.Intent, c.Audience, c.Constraint)
		if err != nil {
			slog.Error("study: build bundle observed conditions failed", "error", err)
			continue
		}
		if len(conds) == 0 || len(members) == 0 {
			continue
		}

		existing, err := s.activationSvc.Store().ListBundlesByStatus(nil, 500, 0)
		if err != nil {
			slog.Error("study: list bundles for dedup check failed", "error", err)
			continue
		}
		querySubject, qi, qa, qc := activation.BuildQueryConditionTerms(c.Subject, c.Intent, c.Audience, c.Constraint)
		var matched *activation.ActivationBundle
		for i := range existing {
			if existing[i].Status == activation.BundleStatusDeprecated {
				continue
			}
			if activation.MatchConditionGroups(existing[i].ObservedConditions, querySubject, qi, qa, qc) {
				matched = &existing[i]
				break
			}
		}

		if matched != nil {
			// RefreshBundleMembers (not UpdateBundleMembers) — this rebuild's
			// members/conds are historical-co-occurrence candidates, not the
			// final state to write: any success_count/failure_count this
			// bundle has already accumulated from live RecordBundleOutcome/
			// RecordMemberOutcome calls must survive the merge, not get reset
			// by this periodic snapshot (docs/impl/v1/activation-bundle.md
			// 「验证」, 2026-08-20 改判).
			if err := s.activationSvc.Store().RefreshBundleMembers(matched.BundleID, members, conds); err != nil {
				slog.Error("study: refresh bundle failed", "bundle_id", matched.BundleID, "error", err)
				continue
			}
			matcher.InvalidateCache()
			continue
		}

		createdFromJSON, err := json.Marshal([]string{})
		if err != nil {
			slog.Error("study: marshal bundle created_from failed", "error", err)
			continue
		}
		b := &activation.ActivationBundle{
			RepresentativeTerms: c.Subject + " " + c.Intent,
			ObservedConditions:  conds,
			Members:             members,
			Status:              activation.BundleStatusCandidate,
			CreatedFrom:         string(createdFromJSON),
		}
		if err := s.activationSvc.Store().CreateBundle(b); err != nil {
			slog.Error("study: create bundle failed", "error", err)
			continue
		}
		matcher.InvalidateCache()
	}

	return s.weakenBundlesWithExpiredCoreMembers()
}

// buildBundleObservedConditionsAndMembers implements docs/impl/v1/
// activation-bundle.md 步骤 4's member-roster reconstruction: pull every
// confident multi-point trace in history, normalize each one's four-tuple
// (scoped to domainIDs, mirroring buildObservedConditions for Link), keep
// only those whose canonical tuple exactly matches (subject,intent,audience,
// constraint), and union their direct_point_ids into the member roster. Each
// member's SuccessCount/FailureCount seed from its appearance ratio across
// the matched traces (same seeding shape as the pre-2026-08-20 residual
// clustering, just grouped by canonical tuple instead of literal text).
func (s *Service) buildBundleObservedConditionsAndMembers(ctx context.Context, subject, intent, audience, constraint string) ([]activation.ObservedCondition, []activation.BundleMember, error) {
	allTraces, err := s.store.AllMultiPointConfidentTraces()
	if err != nil {
		return nil, nil, err
	}

	var matched []BundleScanTraceRow
	for _, t := range allTraces {
		tDomainIDs, err := s.store.DomainIDsForPoints(t.DirectPointIDs)
		if err != nil {
			slog.Error("study: domain ids for member roster trace failed", "trace_id", t.TraceID, "error", err)
			continue
		}
		nSubject, nIntent, nAudience, nConstraint := t.Subject, t.Intent, t.Audience, t.Constraint
		if len(tDomainIDs) > 0 {
			ns, ni, na, nc, err := s.activationSvc.NormalizeTuple(ctx, tDomainIDs, t.Subject, t.Intent, t.Audience, t.Constraint)
			if err != nil {
				slog.Warn("study: member roster tuple normalization failed, using raw quad", "trace_id", t.TraceID, "error", err)
			} else {
				nSubject, nIntent, nAudience, nConstraint = ns, ni, na, nc
			}
		}
		if nSubject == subject && nIntent == intent && nAudience == audience && nConstraint == constraint {
			matched = append(matched, t)
		}
	}
	if len(matched) == 0 {
		return nil, nil, nil
	}

	pointCounts := make(map[string]int)
	for _, t := range matched {
		seen := make(map[string]bool)
		for _, pid := range t.DirectPointIDs {
			if seen[pid] {
				continue
			}
			seen[pid] = true
			pointCounts[pid]++
		}
	}
	total := len(matched)
	pids := make([]string, 0, len(pointCounts))
	for pid := range pointCounts {
		pids = append(pids, pid)
	}
	sort.Strings(pids)
	members := make([]activation.BundleMember, 0, len(pids))
	for _, pid := range pids {
		count := pointCounts[pid]
		members = append(members, activation.BundleMember{
			PointID:      pid,
			SuccessCount: count,
			FailureCount: total - count,
			LastSeenAt:   matched[len(matched)-1].CreatedAt,
		})
	}

	var conds []activation.ObservedCondition
	for _, t := range matched {
		conds = activation.MergeObservedConditions(conds, activation.NormalizeObservedCondition(subject, intent, audience, constraint, "", t.CreatedAt), 50)
	}
	return conds, members, nil
}

// weakenBundlesWithExpiredCoreMembers implements docs/impl/v1/activation-bundle.md
// 步骤 3「lifecycle 驱动的降权」，rewritten 2026-08-13 per
// docs/design/activation-convergence.md「已确认的设计判断」第一条:
// verified 熟路的核心成员 lifecycle 变为非 current，立即单次触发直接置为
// deprecated（weakened 中间态已整体退休，不再经过它），不等窗口统计；
// 路肩成员过期不触发任何状态迁移（下一轮显影扫描的 UpdateBundleMembers
// 自然把它从 fringe 里滤掉）；候选/candidate 状态的熟路核心成员过期时同样
// 不触发（只有 verified 才会被"降级"）。明确不做：熟路成员变化不触发任何
// wiki_pages.needs_recompile 通知——那条已经由 lifecycle.md 步骤 4a 独立
// 覆盖（只认 KP 自身 lifecycle），重复加一条会重复触发同一件事.
//
// 全灭兜底（2026-08-17 补充，见 activation-bundle.md 步骤 3 同一节的补记）：
// 上面的核心/路肩规则对"一条熟路里全部成员都只是路肩、没有任何核心"的情况
// 无法覆盖——它只检查核心成员，路肩全部过期时按原规则不触发任何迁移，于是
// 一条已经没有任何可用知识点的熟路会一直挂着 verified。这里补一条独立于
// 核心/路肩划分的兜底检查：全部成员（不分核心/路肩）都已 lifecycle 非
// current 时，直接置 deprecated——这条不是对原规则的替换，是并列的第二个
// 判据，原有的"核心过期立即降权"规则不变。
func (s *Service) weakenBundlesWithExpiredCoreMembers() error {
	bundles, err := s.activationSvc.Store().ListBundlesByStatus([]string{activation.BundleStatusVerified}, 500, 0)
	if err != nil {
		return err
	}
	confCfg := s.activationSvc.ConfidenceConfig()
	for _, b := range bundles {
		coreExpired := false
		for _, pid := range b.CoreMemberPointIDs(confCfg) {
			current, err := s.store.PointLifecycleCurrent(pid)
			if err != nil {
				slog.Error("study: check bundle core member lifecycle failed", "bundle_id", b.BundleID, "point_id", pid, "error", err)
				continue
			}
			if !current {
				coreExpired = true
				break
			}
		}

		allExpired := len(b.Members) > 0
		for _, m := range b.Members {
			current, err := s.store.PointLifecycleCurrent(m.PointID)
			if err != nil {
				slog.Error("study: check bundle member lifecycle failed", "bundle_id", b.BundleID, "point_id", m.PointID, "error", err)
				allExpired = false
				break
			}
			if current {
				allExpired = false
				break
			}
		}

		if !coreExpired && !allExpired {
			continue
		}
		if err := s.activationSvc.Store().UpdateBundleStatus(b.BundleID, activation.BundleStatusDeprecated); err != nil {
			slog.Error("study: deprecate bundle on member lifecycle expiry failed", "bundle_id", b.BundleID, "error", err)
			continue
		}
		s.activationSvc.BundleMatcher().InvalidateCache()
	}
	return nil
}
