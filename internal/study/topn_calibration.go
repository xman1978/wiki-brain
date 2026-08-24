package study

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jxman78/wiki-brain/internal/retrieval"
)

// —— 阶段 B：从 learning_events 里的 topn_calibration 事件搬到
// topn_calibration_samples（docs/impl/v1/topn-coefficient-convergence.md
// 阶段 B），同一模式镜像 aggregateGaps/FetchUnprocessedActivationGapEvents。

// RawTopNCalibrationEvent is an unprocessed topn_calibration learning_events row.
type RawTopNCalibrationEvent struct {
	EventID string
	TraceID string
	Payload string
}

func (s *Store) FetchUnprocessedTopNCalibrationEvents() ([]RawTopNCalibrationEvent, error) {
	rows, err := s.db.Query(`
		SELECT event_id, trace_id, payload FROM learning_events
		WHERE processed = 0 AND event_type = 'topn_calibration'
		ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("study store: fetch topn_calibration events: %w", err)
	}
	defer rows.Close()

	var events []RawTopNCalibrationEvent
	for rows.Next() {
		var e RawTopNCalibrationEvent
		if err := rows.Scan(&e.EventID, &e.TraceID, &e.Payload); err != nil {
			return nil, fmt.Errorf("study store: scan topn_calibration event: %w", err)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// topNCalibrationEventPayload mirrors trace.generateTopNCalibrationEvent's
// emitted JSON shape.
type topNCalibrationEventPayload struct {
	NAtQueryTime           int                               `json:"n_at_query_time"`
	CoefficientAtQueryTime float64                           `json:"coefficient_at_query_time"`
	CompletenessClass      string                            `json:"completeness_class"`
	RankProxyLower         int                               `json:"rank_proxy_lower"`
	RankProxyIsInterval    bool                              `json:"rank_proxy_is_interval"`
	CandidatePoolSize      int                               `json:"candidate_pool_size"`
	PoolSnapshot           []retrieval.PoolCandidateSnapshot `json:"pool_snapshot,omitempty"`
	CitedUnitIDs           []string                          `json:"cited_unit_ids,omitempty"`
}

// InsertTopNCalibrationSample dedups by trace_id (topn_calibration_event_dedup,
// mirrors cooccurrence_bundle_dedup's INSERT OR IGNORE pattern) then inserts
// into topn_calibration_samples.
func (s *Store) InsertTopNCalibrationSample(traceID string, p topNCalibrationEventPayload) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("study store: insert topn calibration sample begin: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.Exec(`INSERT OR IGNORE INTO topn_calibration_event_dedup (trace_id) VALUES (?)`, traceID)
	if err != nil {
		return fmt.Errorf("study store: topn calibration dedup insert: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Already recorded once — no-op, not an error.
		return tx.Commit()
	}

	var poolJSON, citedJSON sql.NullString
	if len(p.PoolSnapshot) > 0 {
		b, _ := json.Marshal(p.PoolSnapshot)
		poolJSON = sql.NullString{String: string(b), Valid: true}
	}
	if len(p.CitedUnitIDs) > 0 {
		b, _ := json.Marshal(p.CitedUnitIDs)
		citedJSON = sql.NullString{String: string(b), Valid: true}
	}

	isInterval := 0
	if p.RankProxyIsInterval {
		isInterval = 1
	}

	_, err = tx.Exec(`
		INSERT INTO topn_calibration_samples
			(sample_id, trace_id, n_at_query_time, coefficient_at_query_time,
			 completeness_class, rank_proxy_lower, rank_proxy_is_interval,
			 candidate_pool_size, pool_snapshot_json, cited_unit_ids_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uuid.New().String(), traceID, p.NAtQueryTime, p.CoefficientAtQueryTime,
		p.CompletenessClass, p.RankProxyLower, isInterval,
		p.CandidatePoolSize, poolJSON, citedJSON)
	if err != nil {
		return fmt.Errorf("study store: insert topn calibration sample: %w", err)
	}

	return tx.Commit()
}

// processTopNCalibrationEvents drains unprocessed topn_calibration events
// into topn_calibration_samples (docs/impl/v1/topn-coefficient-convergence.md
// 阶段 B). Mirrors aggregateGaps' error handling: a malformed payload is
// logged and the event is still marked processed so it doesn't jam the
// queue.
func (s *Service) processTopNCalibrationEvents() error {
	events, err := s.store.FetchUnprocessedTopNCalibrationEvents()
	if err != nil {
		return err
	}
	for _, e := range events {
		var p topNCalibrationEventPayload
		if err := json.Unmarshal([]byte(e.Payload), &p); err != nil {
			slog.Warn("study: topn_calibration payload malformed", "event_id", e.EventID, "error", err)
		} else if err := s.store.InsertTopNCalibrationSample(e.TraceID, p); err != nil {
			slog.Error("study: insert topn calibration sample failed", "event_id", e.EventID, "error", err)
		}
		if err := s.store.MarkEventProcessed(e.EventID); err != nil {
			return err
		}
	}
	return nil
}

// —— 阶段 C：conformal 分位数 + 系数网格重放（docs/design/topn-coefficient-convergence.md
// 第 4/5 节）——只计算并展示建议值，不接入自动调整。

// TopNCalibrationSample is one row read back from topn_calibration_samples,
// with pool_snapshot/cited_unit_ids decoded.
type TopNCalibrationSample struct {
	SampleID               string
	TraceID                string
	CreatedAt              time.Time
	NAtQueryTime           int
	CoefficientAtQueryTime float64
	CompletenessClass      string
	RankProxyLower         int
	RankProxyIsInterval    bool
	CandidatePoolSize      int
	PoolSnapshot           []retrieval.PoolCandidateSnapshot
	CitedUnitIDs           []string
}

func (s *Store) FetchTopNCalibrationSamples(windowDays int) ([]TopNCalibrationSample, error) {
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT sample_id, trace_id, created_at, n_at_query_time, coefficient_at_query_time,
		       completeness_class, rank_proxy_lower, rank_proxy_is_interval, candidate_pool_size,
		       pool_snapshot_json, cited_unit_ids_json
		FROM topn_calibration_samples
		WHERE created_at >= datetime('now', '-%d days')
		ORDER BY created_at`, windowDays))
	if err != nil {
		return nil, fmt.Errorf("study store: fetch topn calibration samples: %w", err)
	}
	defer rows.Close()

	var out []TopNCalibrationSample
	for rows.Next() {
		var rec TopNCalibrationSample
		var rankProxy sql.NullInt64
		var isInterval int
		var poolJSON, citedJSON sql.NullString
		if err := rows.Scan(&rec.SampleID, &rec.TraceID, &rec.CreatedAt, &rec.NAtQueryTime,
			&rec.CoefficientAtQueryTime, &rec.CompletenessClass, &rankProxy, &isInterval,
			&rec.CandidatePoolSize, &poolJSON, &citedJSON); err != nil {
			return nil, fmt.Errorf("study store: scan topn calibration sample: %w", err)
		}
		if rankProxy.Valid {
			rec.RankProxyLower = int(rankProxy.Int64)
		}
		rec.RankProxyIsInterval = isInterval != 0
		if poolJSON.Valid {
			_ = json.Unmarshal([]byte(poolJSON.String), &rec.PoolSnapshot)
		}
		if citedJSON.Valid {
			_ = json.Unmarshal([]byte(citedJSON.String), &rec.CitedUnitIDs)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// TopNSuggestion is the top-N/目录检索系数自收敛建议
// (docs/design/topn-coefficient-convergence.md) — reporting only.
type TopNSuggestion struct {
	SampleWindowDays    int `json:"sample_window_days"`
	TightCount          int `json:"tight_count"`
	PoolRescuedCount    int `json:"pool_rescued_count"`
	ContentRescuedCount int `json:"content_rescued_count"`
	PoolExhaustedCount  int `json:"pool_exhausted_count"`
	GapAt2NCount        int `json:"gap_at_2n_count"`

	CurrentN           int     `json:"current_n"`
	CurrentCoefficient float64 `json:"current_coefficient"`
	TargetHitRate      float64 `json:"target_hit_rate"`

	SuggestedN               int  `json:"suggested_n"`
	SuggestedNDataSufficient bool `json:"suggested_n_data_sufficient"`

	SuggestedCoefficient               float64 `json:"suggested_coefficient"`
	SuggestedCoefficientDataSufficient bool    `json:"suggested_coefficient_data_sufficient"`

	// 用建议值/当前值回测得到的命中率——在 tight+pool_rescued 校准样本里，
	// 保守代理排名 <= 对应 N 的比例（即 conformal 覆盖率的经验估计）。
	BacktestHitRateAtCurrent   float64 `json:"backtest_hit_rate_at_current"`
	BacktestHitRateAtSuggested float64 `json:"backtest_hit_rate_at_suggested"`
}

func (s *Service) buildTopNSuggestionSection(windowDays int) (TopNSuggestion, error) {
	targetHitRate := s.cfg.TopNTargetHitRate
	if targetHitRate <= 0 || targetHitRate >= 1 {
		targetHitRate = 0.97
	}
	minN := s.cfg.TopNMin
	if minN <= 0 {
		minN = 5
	}
	minCoefficient := s.cfg.TopNCoefficientMin
	if minCoefficient <= 0 {
		minCoefficient = 1
	}
	gridStep := s.cfg.TopNCoefficientGridStep
	if gridStep <= 0 {
		gridStep = 0.1
	}
	gridRadius := s.cfg.TopNCoefficientGridRadius
	if gridRadius <= 0 {
		gridRadius = 5
	}

	currentN := s.currentRerankTopN
	if currentN <= 0 {
		currentN = 20
	}
	currentCoefficient := s.currentOutlineCoefficient
	if currentCoefficient <= 0 {
		currentCoefficient = 1
	}

	out := TopNSuggestion{
		SampleWindowDays:   windowDays,
		CurrentN:           currentN,
		CurrentCoefficient: currentCoefficient,
		TargetHitRate:      targetHitRate,
	}

	samples, err := s.store.FetchTopNCalibrationSamples(windowDays)
	if err != nil {
		return out, err
	}

	var proxies []int // tight + pool_rescued 保守代理值（分位数计算的输入）
	var poolRescued []TopNCalibrationSample
	for _, sample := range samples {
		switch sample.CompletenessClass {
		case retrieval.CompletenessTight:
			out.TightCount++
			proxies = append(proxies, sample.RankProxyLower)
		case retrieval.CompletenessPoolRescued:
			out.PoolRescuedCount++
			proxies = append(proxies, sample.RankProxyLower)
			poolRescued = append(poolRescued, sample)
		case retrieval.CompletenessContentRescued:
			out.ContentRescuedCount++
		case retrieval.CompletenessPoolExhaustedBefore:
			out.PoolExhaustedCount++
		case retrieval.CompletenessGapAt2N:
			out.GapAt2NCount++
		}
	}

	suggestedN, sufficient := conformalQuantile(proxies, targetHitRate, minN)
	out.SuggestedN = suggestedN
	out.SuggestedNDataSufficient = sufficient
	out.BacktestHitRateAtCurrent = backtestHitRate(proxies, currentN)
	if sufficient {
		out.BacktestHitRateAtSuggested = backtestHitRate(proxies, suggestedN)
	}

	suggestedCoefficient, coeffSufficient := suggestCoefficient(poolRescued, currentCoefficient, minCoefficient, gridStep, gridRadius, targetHitRate, minN)
	out.SuggestedCoefficient = suggestedCoefficient
	out.SuggestedCoefficientDataSufficient = coeffSufficient

	return out, nil
}

// conformalQuantile implements split conformal's finite-sample-corrected
// quantile (docs/design/topn-coefficient-convergence.md 第 4 节): the
// ⌈(n+1)(1-α)⌉-th order statistic of proxies, α = 1-targetHitRate. Returns
// sufficient=false (and the input's max, clamped to minN, as a harmless
// placeholder) when there isn't enough data to support the requested
// confidence level — that condition falls straight out of the formula, no
// separate minimum-sample-size threshold needed.
func conformalQuantile(proxies []int, targetHitRate float64, minN int) (n int, sufficient bool) {
	if len(proxies) == 0 {
		return minN, false
	}
	sorted := append([]int(nil), proxies...)
	sort.Ints(sorted)
	count := len(sorted)
	alpha := 1 - targetHitRate
	pos := int(math.Ceil(float64(count+1) * (1 - alpha)))
	if pos > count {
		return minN, false
	}
	if pos < 1 {
		pos = 1
	}
	// Order statistic is a 0-based rank; the smallest N covering it is
	// rank+1.
	suggested := sorted[pos-1] + 1
	if suggested < minN {
		suggested = minN
	}
	return suggested, true
}

// backtestHitRate is the empirical fraction of proxies that would have been
// covered by N (rank_proxy_lower <= N-1, i.e. rank < N) — the conformal
// coverage estimate for a specific N, used to report "如果用这个 N，历史数据
// 上的命中率是多少".
func backtestHitRate(proxies []int, n int) float64 {
	if len(proxies) == 0 {
		return 0
	}
	covered := 0
	for _, p := range proxies {
		if p < n {
			covered++
		}
	}
	return float64(covered) / float64(len(proxies))
}

// suggestCoefficient replays pool_rescued samples' pool snapshots under a
// small grid of candidate coefficients around the current value (small-step
// only — docs/design/topn-coefficient-convergence.md 第 5 节's accepted
// self-selection-bias mitigation, not a wide/line search), picks the
// coefficient whose resulting conformal-quantile N (over the SAME proxy
// definition as conformalQuantile, replayed rank in place of the stored
// interval-upper-bound proxy) is smallest. Returns sufficient=false when
// there are no pool_rescued samples with a usable snapshot — replay needs
// labeled data beyond the original top-N, which only pool_rescued samples
// carry.
func suggestCoefficient(samples []TopNCalibrationSample, current, minCoefficient, gridStep float64, gridRadius int, targetHitRate float64, minN int) (float64, bool) {
	type usable struct {
		pool    []retrieval.PoolCandidateSnapshot
		citedID map[string]bool
	}
	var rows []usable
	for _, s := range samples {
		if len(s.PoolSnapshot) == 0 || len(s.CitedUnitIDs) == 0 {
			continue
		}
		cited := make(map[string]bool, len(s.CitedUnitIDs))
		for _, uid := range s.CitedUnitIDs {
			cited[uid] = true
		}
		rows = append(rows, usable{pool: s.PoolSnapshot, citedID: cited})
	}
	if len(rows) == 0 {
		return current, false
	}

	bestC := current
	bestN := math.MaxInt32
	for step := -gridRadius; step <= gridRadius; step++ {
		c := current + float64(step)*gridStep
		if c < minCoefficient {
			continue
		}
		var proxies []int
		for _, row := range rows {
			proxies = append(proxies, replayWorstCitedRank(row.pool, row.citedID, c))
		}
		n, sufficient := conformalQuantile(proxies, targetHitRate, minN)
		if !sufficient {
			continue
		}
		if n < bestN {
			bestN = n
			bestC = c
		}
	}
	if bestN == math.MaxInt32 {
		return current, false
	}
	return bestC, true
}

// replayWorstCitedRank recomputes RRF scores for one candidate pool under an
// alternate outline_score_coefficient (mirrors rrfMerge's formula exactly:
// score = Σ 1/(RRFK+rank_in_path+1), outline path's term scaled by c) and
// returns the worst (largest) new rank among the pool entries that were
// actually cited for this trace.
func replayWorstCitedRank(pool []retrieval.PoolCandidateSnapshot, cited map[string]bool, coefficient float64) int {
	type scored struct {
		unitID string
		score  float64
	}
	rescored := make([]scored, len(pool))
	for i, c := range pool {
		var score float64
		for path, rank := range c.RankByPath {
			term := 1.0 / float64(retrieval.RRFK+rank+1)
			if path == "outline" {
				term *= coefficient
			}
			score += term
		}
		rescored[i] = scored{unitID: c.UnitID, score: score}
	}
	sort.Slice(rescored, func(i, j int) bool {
		if rescored[i].score != rescored[j].score {
			return rescored[i].score > rescored[j].score
		}
		return rescored[i].unitID < rescored[j].unitID
	})

	worst := 0
	for rank, r := range rescored {
		if cited[r.unitID] && rank > worst {
			worst = rank
		}
	}
	return worst
}
