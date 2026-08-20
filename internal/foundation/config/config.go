package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Logging   LoggingConfig   `yaml:"logging"`
	Database  DatabaseConfig  `yaml:"database"`
	Index     IndexConfig     `yaml:"index"`
	Queue     QueueConfig     `yaml:"queue"`
	FileView  FileViewConfig  `yaml:"fileview"`
	Source    SourceConfig    `yaml:"source"`
	Retrieval RetrievalConfig `yaml:"retrieval"`
	Study     StudyConfig     `yaml:"study"`
	Evidence  EvidenceConfig  `yaml:"evidence"`
	KPN       KPNConfig       `yaml:"kpn"`
	Wiki      WikiConfig      `yaml:"wiki"`

	// BootstrapLLM is populated from an optional llm: section in config.yml
	// only for one-time import into the database at startup. Not used at runtime.
	BootstrapLLM *BootstrapLLM `yaml:"-"`
}

// BootstrapLLM is the legacy config.yml llm block used only for first-start import.
type BootstrapLLM struct {
	BaseURL        string                    `yaml:"base_url"`
	APIKey         string                    `yaml:"api_key"`
	TimeoutSeconds int                       `yaml:"timeout_seconds"`
	MaxRetries     int                       `yaml:"max_retries"`
	Models         map[string]BootstrapModel `yaml:"models"`
}

type BootstrapModel struct {
	Model           string  `yaml:"model"`
	Temperature     float64 `yaml:"temperature"`
	MaxInputTokens  int     `yaml:"max_input_tokens"`
	MaxOutputTokens int     `yaml:"max_output_tokens"`
	Thinking        bool    `yaml:"thinking"`
}

type ServerConfig struct {
	Host           string `yaml:"host"`
	Port           int    `yaml:"port"`
	PathPrefix     string `yaml:"path_prefix"`
	ReadTimeout    string `yaml:"read_timeout"`
	WriteTimeout   string `yaml:"write_timeout"`
	MaxConcurrency int    `yaml:"max_concurrency"`
}

// LoggingConfig — 日志级别、输出目的地、轮转策略配置（docs/impl/mvp/foundation.md 步骤 7）。
type LoggingConfig struct {
	Level         string `yaml:"level"`          // debug / info / warn / error
	Dir           string `yaml:"dir"`            // 日志文件存放目录
	Filename      string `yaml:"filename"`       // 日志文件名
	Console       bool   `yaml:"console"`        // 业务日志是否输出到控制台
	File          bool   `yaml:"file"`           // 是否输出到文件
	MaxSizeMB     int    `yaml:"max_size_mb"`    // 单个日志文件大小上限（MB），超出后轮转
	MaxBackups    int    `yaml:"max_backups"`    // 保留的历史轮转文件数量，0 表示不限制
	MaxAgeDays    int    `yaml:"max_age_days"`   // 历史轮转文件保留天数，0 表示不按天数清理
	Compress      bool   `yaml:"compress"`       // 轮转后的历史文件是否压缩（gzip）
	AccessConsole bool   `yaml:"access_console"` // 访问日志（http request）是否额外打印到控制台
}

// ParseLevel 将配置的字符串日志级别解析为 slog.Level，无法识别时回退为 info。
func (c *LoggingConfig) ParseLevel() slog.Level {
	switch strings.ToLower(c.Level) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

type IndexConfig struct {
	Path string `yaml:"path"`
}

type QueueConfig struct {
	BufferSize int `yaml:"buffer_size"`
	Workers    int `yaml:"workers"`
}

type FileViewConfig struct {
	BaseURL        string `yaml:"base_url"`
	PollIntervalMs int    `yaml:"poll_interval_ms"`
	MaxPollSeconds int    `yaml:"max_poll_seconds"`
}

type SourceConfig struct {
	UploadDir       string `yaml:"upload_dir"`
	SegmentMaxChars int    `yaml:"segment_max_chars"`
	MinSegmentChars int    `yaml:"min_segment_chars"`
	// Unit extraction always runs the pre-insert dedup path: segments are
	// extracted concurrently, then candidates are gap-filled and deduplicated
	// in memory before any store/index writes happen.
	// PreInsertDedupMinOverlap is the minimum token containment coefficient (0-1)
	// between two candidate units' text for the pair to be worth an
	// unit_dedup.md call; below it they're skipped as clearly unrelated.
	// Defaults to 0.15 when unset (<=0).
	PreInsertDedupMinOverlap float64 `yaml:"pre_insert_dedup_min_overlap"`
	// PreInsertDedupConcurrency is how many segments have their extraction
	// call in flight at once under the bypass. Defaults to 2 when unset (<=0).
	PreInsertDedupConcurrency int `yaml:"pre_insert_dedup_concurrency"`
	// PreInsertDedupShortTokenMax: a candidate pair where either side's text
	// has at most this many unique tokens always gets an unit_dedup.md call,
	// skipping the overlap gate entirely. A short lead-in (a heading or a
	// code comment) can share zero literal vocabulary with the content it
	// introduces — e.g. a Chinese comment above an English/SQL command has
	// no token overlap with it under any set-similarity formula — so the
	// overlap gate is unreliable exactly where short-vs-long pairs matter
	// most. Defaults to 4 when unset (<=0).
	PreInsertDedupShortTokenMax int `yaml:"pre_insert_dedup_short_token_max"`
}

type RetrievalConfig struct {
	OutlineFTSMinScore         float64 `yaml:"outline_fts_min_score"`
	RerankTopN                 int     `yaml:"rerank_top_n"`
	RerankExtractBatchMaxChars int     `yaml:"rerank_extract_batch_max_chars"`
	RerankExtractBatchMaxUnits int     `yaml:"rerank_extract_batch_max_units"`
	RerankExtractConcurrency   int     `yaml:"rerank_extract_concurrency"`
	RerankJudgeBatchMaxChars   int     `yaml:"rerank_judge_batch_max_chars"`
	RerankJudgeConcurrency     int     `yaml:"rerank_judge_concurrency"`
	ActivationMatchTop         int     `yaml:"activation_match_top"`
	// OutlineRRFBoost multiplies the RRF score contributed by the outline
	// (目录) recall path in rrfMerge, to reflect its higher observed hit rate
	// vs. fts/fts_tuple. 1.0 or unset (<=0) means no boost (RRF unweighted).
	OutlineRRFBoost float64 `yaml:"outline_rrf_boost"`
	// —— V1 新增（docs/impl/v1/retrieval.md 配置项）——
	FastPath       bool `yaml:"fast_path"`
	FastPathVerify bool `yaml:"fast_path_verify"`
	// SlowPathVerify gates a sufficiency check on PathType=full evidence
	// before Answer generates (docs/impl/v1/retrieval.md 步骤 2b). When
	// sufficient=false the answer layer refuses with the no-evidence
	// fallback instead of letting near-miss evidence (wrong system /
	// wrong intent) be rewritten into a confident answer.
	SlowPathVerify   bool    `yaml:"slow_path_verify"`
	FastPathFallback bool    `yaml:"fast_path_fallback"`
	WikiMinScore     float64 `yaml:"wiki_min_score"`
	// WikiMaxCandidates caps how many wiki-index/concept-matched candidate
	// pages TryDirectAnswer tries in order before falling through
	// (docs/impl/v1/wiki.md 步骤 4; <=0 defaults to 3, 1 reproduces the
	// original top-1-only behavior).
	WikiMaxCandidates int `yaml:"wiki_max_candidates"`
	// RerankRelevanceConcise 控制证据过滤阶段（rerank_relevance）要不要求模型
	// 输出 analysis 分析字段。false（默认，含零值）用 rerank_relevance.md，
	// 输出结果附一句话依据，便于调试排查；true 用 rerank_relevance_concise.md，
	// 只输出 candidate_id/relevant，省去分析文本以缩短响应耗时。
	RerankRelevanceConcise bool `yaml:"rerank_relevance_concise"`

	// —— 问题四元组归一化（2026-08-12 新增，docs/impl/v1/retrieval.md 步骤 2）——
	// 默认关闭：新机制，先不改变现有行为，观测后再打开。
	QuestionTupleNormEnabled bool `yaml:"question_tuple_norm_enabled"`
	// QuestionTupleNormLocalSimMin 是 Tier 2（本地词集 Jaccard 相似度）的命中
	// 阈值，0-1。
	QuestionTupleNormLocalSimMin float64 `yaml:"question_tuple_norm_local_sim_min"`
	// QuestionTupleNormIdleDays：question_tuple_norms 行 last_hit_at 超过此
	// 天数未再命中，由 Study 周期清理（study.md 步骤 4 同款 idle 清理）。
	QuestionTupleNormIdleDays int `yaml:"question_tuple_norm_idle_days"`

	// —— ActivationLink/Bundle 连续置信度（2026-08-13 新增，
	// docs/impl/v1/activation.md 配置项）——
	ServingConfidenceMin  float64 `yaml:"serving_confidence_min"`
	AuditSampleMin        int     `yaml:"audit_sample_min"`
	ExploreRateLow        float64 `yaml:"explore_rate_low"`
	ExploreRateSelfGraded float64 `yaml:"explore_rate_self_graded"`
	ExploreRateTrusted    float64 `yaml:"explore_rate_trusted"`
}

// EvidenceConfig — docs/impl/v1/evidence.md 配置项.
type EvidenceConfig struct {
	Enabled           bool `yaml:"enabled"`
	BatchMaxChars     int  `yaml:"batch_max_chars"`
	MaxFragmentsPerKU int  `yaml:"max_fragments_per_ku"`
	MinFragmentChars  int  `yaml:"min_fragment_chars"`
	Retry             int  `yaml:"retry"`
	// Concurrency bounds how many batches Mine() runs in parallel
	// (docs/impl/v1/evidence.md 步骤 1: "批次串行或并发执行均可，并发受
	// llm.max_concurrency 约束"). <=0 defaults to 4, mirroring
	// RetrievalConfig.RerankJudgeConcurrency's resolution pattern.
	Concurrency int `yaml:"concurrency"`
}

// KPNConfig — docs/impl/v1/kpn.md 配置项.
type KPNConfig struct {
	CrossMaxBatches int `yaml:"cross_max_batches"`
}

// WikiConfig — docs/impl/v1/wiki.md 配置项.
type WikiConfig struct {
	CompileMaxChars   int `yaml:"compile_max_chars"`
	RecompileNewKPMin int `yaml:"recompile_new_kp_min"`
	// TriggerQuestionsMax caps aliases and trigger_questions each (<=0
	// defaults to 10; docs/impl/v1/wiki.md 步骤 3).
	TriggerQuestionsMax int `yaml:"trigger_questions_max"`
	// QualifyingMinDaysActive gates the concept-level "ready" recommendation
	// on activity span, not just count (docs/design/wiki-compilation.md
	// "反复激活、多次验证、持续采用不是命中次数"; docs/impl/v1/wiki.md 步骤 3
	// "概念级 ready 判定").
	QualifyingMinDaysActive int `yaml:"qualifying_min_days_active"`

	// —— 两层架构（docs/impl/v1/wiki.md 步骤 7-9）——
	// 2026-08-18 单层化收尾清理: TopicMemberMin/TopicCompileMaxChars/
	// TopicCandidateKPMax/TopicReliabilityMin/TopicRerankBatchMaxChars
	// (两层架构主题页专属阈值) removed — 已确认代码中无引用
	// (docs/impl/v1/wiki-single-tier-open-questions.md).
	RelationKPNMin         int `yaml:"relation_kpn_min"`
	RelationSharedPointMin int `yaml:"relation_shared_point_min"`

	// —— 生成质量（docs/impl/v1/wiki-generation.md 阶段 E/G，P0）——
	// ClaimVerifyEnabled toggles the post-compile support check (阶段 E):
	// does each claim's text actually hold up against the KP material it
	// cites, not just "are the cited point_ids in-bounds". Off by default
	// behavior is unaffected (existing citation whitelist checks still run).
	ClaimVerifyEnabled bool `yaml:"claim_verify_enabled"`
	// SelfcheckEnabled toggles the pre-publish quality gate (阶段 G): replay
	// real confident questions this page's KPs were once answered from
	// against the compiled page itself, via the existing answer_wiki path.
	SelfcheckEnabled bool `yaml:"selfcheck_enabled"`
	// SelfcheckReplayN is how many sampled confident questions to replay
	// per page (<=0 defaults to 5).
	SelfcheckReplayN int `yaml:"selfcheck_replay_n"`
	// SelfcheckMinSufficientRate: publish is blocked (absent force=true) if
	// the replay's sufficient=true rate falls below this.
	SelfcheckMinSufficientRate float64 `yaml:"selfcheck_min_sufficient_rate"`
	// SelfcheckMinMaterialUsage: |source_point_ids| / |qualifying KP| must be
	// at least this — a low ratio means the page left most of its qualifying
	// material unused.
	SelfcheckMinMaterialUsage float64 `yaml:"selfcheck_min_material_usage"`
	// SelfcheckMaxUncitedRate caps the share of sentences in the stable-
	// conclusions/expanded-explanation sections that carry no [point_id] tag.
	SelfcheckMaxUncitedRate float64 `yaml:"selfcheck_max_uncited_rate"`

	// —— 综合满意度轴（synthesis satisfaction，docs/impl/v1/wiki.md 步骤
	// 4a，2026-08-13 新增）——
	// SynthesisAuditRate is the per-served-direct-answer sampling rate for
	// the independent-verification trial that updates wiki_pages'
	// synthesis_{success,failure,audited_success,audited_failure}_count
	// columns — audit-only by design (no self-graded tier, unlike
	// ActivationLink/Bundle): a served answer that isn't sampled produces no
	// synthesis event at all, see wiki.md 步骤 4a「未中选」.
	SynthesisAuditRate float64 `yaml:"synthesis_audit_rate"`
}

type StudyConfig struct {
	ScheduleInterval string `yaml:"schedule_interval"`
	// candidate_confident_min/candidate_ratio_min 已删除（2026-08-13，随
	// docs/design/activation-convergence.md 第 11 节一并替换——创建门槛换成
	// 与「收敛剪枝」同一套 Beta 均值/宽度公式，见 CreateConfidenceMin/
	// CreateWidthMax，docs/impl/v1/study.md 步骤 1）
	CreateConfidenceMin float64 `yaml:"create_confidence_min"`
	CreateWidthMax      float64 `yaml:"create_width_max"`
	WikiKPMin           int     `yaml:"wiki_kp_min"`
	GapHitThreshold     int     `yaml:"gap_hit_threshold"`
	ScanBatchSize       int     `yaml:"scan_batch_size"`
	ReportPeriodDays    int     `yaml:"report_period_days"`
	ReportMaxKeep       int     `yaml:"report_max_keep"`
	// —— V1 新增（docs/impl/v1/study.md 配置项）——
	// 2026-08-13：auto_promote/promote_*/weaken_*/reverify_*/candidate_idle_days/
	// deprecate_idle_days 随离散状态机一起删除（docs/design/
	// activation-convergence.md, docs/impl/v1/activation.md「移除的旧配置项」）
	// ——不再有"晋升/降权/重新验证/闲置淘汰"这些离散判定，替代方式是
	// retrieval.{serving_confidence_min,audit_sample_min,explore_rate_*} 五项
	// + Match()/RecordOutcome 的连续置信度计算，见 RetrievalConfig。
	EventWindowDays  int `yaml:"event_window_days"`
	CorrectionWeight int `yaml:"correction_weight"`
	// —— 收敛剪枝（2026-08-13 新增，docs/impl/v1/study.md 步骤 3）——
	// candidate_idle_days/deprecate_idle_days 已删除（随离散状态机一起废弃
	// ——淘汰粒度下沉到单条观测条件，见下方 prune_* 四项）。
	PruneMeanMax   float64 `yaml:"prune_mean_max"`
	PruneWidthMax  float64 `yaml:"prune_width_max"`
	PruneSampleMin int     `yaml:"prune_sample_min"`
	PruneIdleDays  int     `yaml:"prune_idle_days"`
	PruneStaleDays int     `yaml:"prune_stale_days"`
	// ObservedConditionsMax caps ActivationLink observed_conditions groups
	// (docs/superpowers/specs/2026-07-22-activation-observed-conditions-design.md).
	ObservedConditionsMax int `yaml:"observed_conditions_max"`
	// —— subject 同义词挖掘（V1 新增，
	// docs/superpowers/specs/2026-07-24-activation-subject-synonym-design.md）——
	SynonymGapMin         int  `yaml:"synonym_gap_min"`
	SynonymGapDistinctMin int  `yaml:"synonym_gap_distinct_min"`
	SynonymAutoPromote    bool `yaml:"synonym_auto_promote"`
	// —— 概念演化（V1 新增，docs/impl/v1/concept-evolution.md 配置项）——
	EntryNullRatioMin      float64 `yaml:"entry_null_ratio_min"`
	EntryAddEventMin       int     `yaml:"entry_add_event_min"`
	EntryAddDistinctMin    int     `yaml:"entry_add_distinct_min"`
	EntryAddOverlapMin     float64 `yaml:"entry_add_overlap_min"`
	EntryMergeCooccurMin   int     `yaml:"entry_merge_cooccur_min"`
	EntryMergeOverlapMin   float64 `yaml:"entry_merge_overlap_min"`
	EntryCandidateIdleDays int     `yaml:"entry_candidate_idle_days"`
	EntryEventWindowDays   int     `yaml:"entry_event_window_days"`
	EntryAddAutoConfirm    bool    `yaml:"entry_add_auto_confirm"`
	// —— 问题复杂度观测量（两层架构扩展，docs/impl/v1/study.md 步骤 7）——
	ComplexityMinQuestions int `yaml:"complexity_min_questions"`
	// —— ActivationBundle（熟路）阶段 1（docs/impl/v1/activation-bundle.md）——
	// 2026-08-20 重设计：生成门槛复用 CreateConfidenceMin/CreateWidthMax
	// （不新增配置），核心/路肩完全由 BundleMember 自己的置信度轴派生，
	// BundleCoreRatioMin/BundleClusterMinQuestions/BundleClusterMinDaysActive/
	// BundleCoreSizeMax 四个字段随旧的四元组聚类生成机制一并删除。
	BundlePromoteSuccessMin  int     `yaml:"bundle_promote_success_min"`
	BundlePromoteDistinctMin int     `yaml:"bundle_promote_distinct_min"`
	BundleWeakenFailureMin   int     `yaml:"bundle_weaken_failure_min"`
	BundleWeakenRatioMin     float64 `yaml:"bundle_weaken_ratio_min"`
	BundleReverifySuccessMin int     `yaml:"bundle_reverify_success_min"`
	BundleCandidateIdleDays  int     `yaml:"bundle_candidate_idle_days"`
	BundleDeprecateIdleDays  int     `yaml:"bundle_deprecate_idle_days"`
	BundleAutoPromote        bool    `yaml:"bundle_auto_promote"`
}

func Load(configPath string) (*Config, error) {
	path, err := findConfigFile(configPath)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	expanded := os.ExpandEnv(string(data))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	var bootstrap struct {
		LLM *BootstrapLLM `yaml:"llm"`
	}
	if err := yaml.Unmarshal([]byte(expanded), &bootstrap); err == nil {
		cfg.BootstrapLLM = bootstrap.LLM
	}

	applyLoggingDefaults(&cfg)
	applyEnvOverrides(&cfg)

	return &cfg, nil
}

func applyLoggingDefaults(cfg *Config) {
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.Logging.Dir == "" {
		cfg.Logging.Dir = "logs"
	}
	if cfg.Logging.Filename == "" {
		cfg.Logging.Filename = "wiki-brain.log"
	}
}

func findConfigFile(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("config: specified file not found: %s", explicit)
		}
		return explicit, nil
	}

	if envPath := os.Getenv("WIKI_CONFIG_PATH"); envPath != "" {
		if _, err := os.Stat(envPath); err == nil {
			return envPath, nil
		}
	}

	if _, err := os.Stat("./config.yml"); err == nil {
		return "./config.yml", nil
	}

	home, err := os.UserHomeDir()
	if err == nil {
		p := home + "/.wiki/config.yml"
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	return "", fmt.Errorf("config: no config file found (tried --config, WIKI_CONFIG_PATH, ./config.yml, ~/.wiki/config.yml)")
}

func applyEnvOverrides(cfg *Config) {
	overrides := map[string]*string{
		"WB_DATABASE_PATH":        &cfg.Database.Path,
		"WB_INDEX_PATH":           &cfg.Index.Path,
		"WB_SOURCE_UPLOAD_DIR":    &cfg.Source.UploadDir,
		"WB_SERVER_HOST":          &cfg.Server.Host,
		"WB_SERVER_PATH_PREFIX":   &cfg.Server.PathPrefix,
		"WB_SERVER_READ_TIMEOUT":  &cfg.Server.ReadTimeout,
		"WB_SERVER_WRITE_TIMEOUT": &cfg.Server.WriteTimeout,
		"WB_LOGGING_LEVEL":        &cfg.Logging.Level,
		"WB_LOGGING_DIR":          &cfg.Logging.Dir,
		"WB_LOGGING_FILENAME":     &cfg.Logging.Filename,
	}

	for env, ptr := range overrides {
		if val := os.Getenv(env); val != "" {
			*ptr = val
		}
	}

	intOverrides := map[string]*int{
		"WB_SERVER_PORT":            &cfg.Server.Port,
		"WB_SERVER_MAX_CONCURRENCY": &cfg.Server.MaxConcurrency,
		"WB_QUEUE_BUFFER_SIZE":      &cfg.Queue.BufferSize,
		"WB_QUEUE_WORKERS":          &cfg.Queue.Workers,
		"WB_LOGGING_MAX_SIZE_MB":    &cfg.Logging.MaxSizeMB,
		"WB_LOGGING_MAX_BACKUPS":    &cfg.Logging.MaxBackups,
		"WB_LOGGING_MAX_AGE_DAYS":   &cfg.Logging.MaxAgeDays,
	}

	for env, ptr := range intOverrides {
		if val := os.Getenv(env); val != "" {
			var n int
			if _, err := fmt.Sscanf(val, "%d", &n); err == nil {
				*ptr = n
			}
		}
	}

	// PORT is the conventional env var dev-preview tooling assigns a free
	// port through; only honored when WB_SERVER_PORT (this project's own
	// override) wasn't already set, so it never fights an explicit choice.
	if os.Getenv("WB_SERVER_PORT") == "" {
		if val := os.Getenv("PORT"); val != "" {
			var n int
			if _, err := fmt.Sscanf(val, "%d", &n); err == nil {
				cfg.Server.Port = n
			}
		}
	}

	_ = strings.Contains // ensure import used
}
