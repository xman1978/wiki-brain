package study

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sort"

	"github.com/jxman78/wiki-brain/internal/activation"
	"github.com/jxman78/wiki-brain/internal/session"
)

// scanActivationBundles implements docs/impl/v1/activation-bundle.md 步骤 4
// 的显影扫描（Study 步骤 5b）: 逐条 confident 多点 trace 先试着被已有熟路
// Match 吸收（同一类问题的解析抖动不应分裂成两条熟路，2026-08-11 决策），
// 未命中的落入残余池，按归一化四元组聚类，达标的分组核实核心/路肩比例后
// 建新熟路。阶段 1 不产生 bundle_success/bundle_failure 事件，因此新建的
// bundle 停留在 candidate（预期行为，docs/impl/v1/activation-bundle.md 步骤 6）。
func (s *Service) scanActivationBundles() error {
	if s.activationSvc == nil {
		return nil
	}
	rows, err := s.store.BundleScanTraces(s.cfg.EventWindowDays)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}

	matcher := s.activationSvc.BundleMatcher()
	ctx := context.Background()
	matchCfg := activation.MatchConfig{}

	type quadKey struct{ subject, intent, audience, constraint string }
	type quadAgg struct {
		traces      []BundleScanTraceRow
		questions   map[string]bool
		dates       map[string]bool
		pointCounts map[string]int
	}
	residual := make(map[quadKey]*quadAgg)
	var order []quadKey

	for _, r := range rows {
		q := session.ExpandedQuery{ExpandedQuestion: r.Question, Subject: r.Subject, Intent: r.Intent, Audience: r.Audience, Constraint: r.Constraint}
		matches, err := s.activationSvc.MatchBundles(ctx, q, matchCfg)
		if err != nil {
			slog.Error("study: bundle scan match failed", "trace_id", r.TraceID, "error", err)
			continue
		}
		if len(matches) > 0 {
			// 命中已有熟路：合并观测条件、记录一次使用，不重复分组。
			b := matches[0].Bundle
			cond := activation.NormalizeObservedCondition(r.Subject, r.Intent, r.Audience, r.Constraint, "", r.CreatedAt)
			merged := activation.MergeObservedConditions(b.ObservedConditions, cond, 50)
			if err := s.activationSvc.Store().UpdateBundleMembers(b.BundleID, b.Members, merged); err != nil {
				slog.Error("study: update matched bundle conditions failed", "bundle_id", b.BundleID, "error", err)
				continue
			}
			if err := s.activationSvc.Store().TouchBundleLastUsed(b.BundleID); err != nil {
				slog.Error("study: touch bundle last used failed", "bundle_id", b.BundleID, "error", err)
			}
			matcher.InvalidateCache()
			continue
		}

		k := quadKey{r.Subject, r.Intent, r.Audience, r.Constraint}
		a, ok := residual[k]
		if !ok {
			a = &quadAgg{questions: map[string]bool{}, dates: map[string]bool{}, pointCounts: map[string]int{}}
			residual[k] = a
			order = append(order, k)
		}
		a.traces = append(a.traces, r)
		a.questions[r.Question] = true
		a.dates[r.CreatedAt.UTC().Format("2006-01-02")] = true
		seenInThisTrace := map[string]bool{}
		for _, pid := range r.DirectPointIDs {
			if seenInThisTrace[pid] {
				continue
			}
			seenInThisTrace[pid] = true
			a.pointCounts[pid]++
		}
	}

	minQuestions := s.cfg.BundleClusterMinQuestions
	if minQuestions <= 0 {
		minQuestions = 3
	}
	minDaysActive := s.cfg.BundleClusterMinDaysActive
	if minDaysActive <= 0 {
		minDaysActive = 7
	}
	coreRatioMin := s.cfg.BundleCoreRatioMin
	if coreRatioMin <= 0 {
		coreRatioMin = 0.5
	}
	coreSizeMax := s.cfg.BundleCoreSizeMax
	if coreSizeMax <= 0 {
		coreSizeMax = 8
	}

	for _, k := range order {
		a := residual[k]
		distinctQuestionCount := len(a.questions)
		daysActive := len(a.dates)
		if distinctQuestionCount < minQuestions || daysActive < minDaysActive {
			continue
		}

		// 出现比例仍然用于种子化每个成员的 success_count/failure_count
		// （2026-08-13 编注："出现比例 → member/fringe" 这条判定已被成员置信度
		// 取代——成员是否算"核心"不再是这里写死的标签，是 conditionTier 对
		// (SuccessCount,FailureCount) 求值的持续结果，见 bundle_types.go
		// CoreMemberPointIDs）；这里的 coreRatioMin 只保留用于下方创建门槛
		// "member 非空/member 大小上限" 这两项结构性检查，检查对象换成按
		// 出现比例算出的临时 core 集合（不落库，只是这一步骤判断该不该建
		// bundle 用的辅助量）。
		total := len(a.traces)
		var members []activation.BundleMember
		var core []string
		pids := make([]string, 0, len(a.pointCounts))
		for pid := range a.pointCounts {
			pids = append(pids, pid)
		}
		sort.Strings(pids)
		for _, pid := range pids {
			count := a.pointCounts[pid]
			ratio := float64(count) / float64(total)
			if ratio >= coreRatioMin {
				core = append(core, pid)
			}
			members = append(members, activation.BundleMember{
				PointID:      pid,
				SuccessCount: count,
				FailureCount: total - count,
			})
		}

		if len(core) == 0 || len(core) > coreSizeMax {
			// 核心为空（没有稳定共现的知识点）或核过宽（覆盖面太散，不是一
			// 条真正的熟路）——只作为观测量，不建 bundle（阶段 1 无独立报告
			// 项承接，未来若需要可比照 flagTopicPageCandidates 的
			// TopicSignalUnderfilled 补一条）。
			continue
		}

		fingerprint := bundleFingerprint(k.subject, k.intent, k.audience, k.constraint)
		// 防御性复核：即使残余池已经过 Match 未命中筛选，两次扫描之间可能有
		// 并发写入或上一批刚建的 bundle 尚未反映到缓存，重新查一遍非
		// deprecated 的同指纹 bundle，避免重复创建。
		existing, err := s.activationSvc.Store().ListBundlesByStatus(nil, 500, 0)
		if err != nil {
			slog.Error("study: list bundles for dedup check failed", "error", err)
			continue
		}
		dup := false
		for _, b := range existing {
			if b.ClusterFingerprint == fingerprint {
				dup = true
				break
			}
		}
		if dup {
			continue
		}

		var conds []activation.ObservedCondition
		for _, t := range a.traces {
			conds = activation.MergeObservedConditions(conds, activation.NormalizeObservedCondition(t.Subject, t.Intent, t.Audience, t.Constraint, "", t.CreatedAt), 50)
		}
		traceIDs := make([]string, 0, len(a.traces))
		for _, t := range a.traces {
			traceIDs = append(traceIDs, t.TraceID)
		}
		createdFromJSON, err := json.Marshal(traceIDs)
		if err != nil {
			slog.Error("study: marshal bundle created_from failed", "error", err)
			continue
		}

		b := &activation.ActivationBundle{
			ClusterFingerprint:  fingerprint,
			RepresentativeTerms: k.subject + " " + k.intent,
			ObservedConditions:  conds,
			Members:             members,
			Status:              activation.BundleStatusCandidate,
			CreatedFrom:         string(createdFromJSON),
		}
		if err := s.activationSvc.Store().CreateBundle(b); err != nil {
			slog.Error("study: create bundle failed", "fingerprint", fingerprint, "error", err)
			continue
		}
		matcher.InvalidateCache()
	}

	return s.weakenBundlesWithExpiredCoreMembers()
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

func bundleFingerprint(subject, intent, audience, constraint string) string {
	h := sha1.Sum([]byte(subject + "\x1f" + intent + "\x1f" + audience + "\x1f" + constraint))
	return hex.EncodeToString(h[:])
}
