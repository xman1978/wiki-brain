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
	// —— V1 新增（docs/impl/v1/retrieval.md 配置项）——
	FastPath         bool    `yaml:"fast_path"`
	FastPathVerify   bool    `yaml:"fast_path_verify"`
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
	// RerankJudgeIncludeAnalysis toggles whether the rerank judge LLM call
	// is asked to also produce a per-candidate `analysis` explanation
	// (used only for debug logging, not decision logic — see
	// internal/retrieval/service.go judgeExtractedEvidence). A *bool
	// (rather than plain bool) so an absent key in config.yml is
	// distinguishable from an explicit false: nil means "unset" and keeps
	// the historical behavior (include analysis), matching how
	// rerankJudgeIncludeAnalysis() resolves it. Set to false to A/B test
	// whether dropping the analysis field speeds up rerank latency.
	RerankJudgeIncludeAnalysis *bool `yaml:"rerank_judge_include_analysis"`
	// RerankTwoStep gates an experimental split of the single rerank_judge
	// call (relevance + direct/supporting classification done together) into
	// two sequential calls: rerank_relevance.md (relevant/irrelevant only,
	// same object/scenario hard gate) then rerank_classify.md (direct/
	// supporting, only over candidates already confirmed relevant). Default
	// false — this is a side path being validated against the combined
	// prompt before promotion, not yet the default (2026-08-08 决策: 先旁路
	// 验证效果，确认稳定后再转正，替换 rerank_judge.md 单次调用).
	RerankTwoStep bool `yaml:"rerank_two_step"`
	// SkeletonInjectionEnabled gates topic-page skeleton injection into the
	// slow path (docs/impl/v1/wiki.md 步骤 8「检索接入」, docs/impl/v1/wiki.md
	// 两层架构扩展): default false — injection stakes recall quality on how
	// well a topic page's member boundary was drawn, and a too-narrow
	// boundary silently degrades recall instead of failing loud. Turn on
	// after observing resolved_outside_count (docs/impl/v1/study.md 步骤 7).
	SkeletonInjectionEnabled bool `yaml:"skeleton_injection_enabled"`
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
	RelationKPNMin         int `yaml:"relation_kpn_min"`
	RelationSharedPointMin int `yaml:"relation_shared_point_min"`
	// TopicMemberMin is, since the 2026-08-03 revision, ONLY the
	// recompile-time minimum remaining member gate (RecompileTopic) — it no
	// longer doubles as a candidate-creation threshold, because candidate
	// range is now determined by quadruple clustering over real questions,
	// not by connected-component size (docs/impl/v1/wiki.md 步骤 8).
	TopicMemberMin       int `yaml:"topic_member_min"`
	TopicCompileMaxChars int `yaml:"topic_compile_max_chars"`

	// —— 主题候选识别（docs/impl/v1/wiki.md 步骤 8，2026-08-03 修订：四元组
	// 聚类替代连通分量）——
	// TopicClusterMinQuestions/TopicClusterMinDaysActive gate "稳定簇判定":
	// a normalized (subject,intent,audience,constraint_text) trace group must
	// clear both before it's even considered a topic candidate.
	TopicClusterMinQuestions  int `yaml:"topic_cluster_min_questions"`
	TopicClusterMinDaysActive int `yaml:"topic_cluster_min_days_active"`
	// TopicCandidateKPMax caps the candidate-range semantic KP retrieval
	// (步骤 8 第 3 步), score-descending.
	TopicCandidateKPMax int `yaml:"topic_candidate_kp_max"`
	// TopicReliabilityMin gates 二阶准入的"整体可靠度": the fraction of the
	// full candidate-range KP set (not just the qualifying subset) that has
	// a verified ActivationLink.
	TopicReliabilityMin float64 `yaml:"topic_reliability_min"`
	// TopicRerankBatchMaxChars caps each LLM relevance-judge batch's total
	// candidate content size for retrieveAndGroupQualifyingKPs's manual-
	// trigger candidate search (docs/impl/v1/wiki.md 步骤 8 "人工手动指定
	// 主题" 2026-08-07 修订). <=0 defaults to 6000.
	TopicRerankBatchMaxChars int `yaml:"topic_rerank_batch_max_chars"`

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

	// —— 概念内聚度（docs/impl/v1/wiki-generation.md 2.2/2.4，P0/P1 共用的
	// Louvain 社区检测基础设施；概念级 ready 判定第五项） ——
	// EntryCohesionMin gates the Study wiki-candidate "ready" recommendation
	// on the largest Louvain community's share of qualifying KPs — a low
	// share means the concept's qualifying material splits into several
	// unrelated clusters rather than one coherent topic
	// (docs/design/wiki-compilation.md "连贯性判断还需要第三层").
	EntryCohesionMin float64 `yaml:"entry_cohesion_min"`
	// AspectWRel/AspectWCooc are edge weights feeding the concept-cohesion
	// graph: KPN related/contradicts relations (both count positive — see
	// docs/impl/v1/wiki-generation.md 2.1 "contradicts 计正权") and shared
	// confident-question co-occurrence, saturating at AspectCoocSat.
	AspectWRel    float64 `yaml:"aspect_w_rel"`
	AspectWCooc   float64 `yaml:"aspect_w_cooc"`
	AspectCoocSat int     `yaml:"aspect_cooc_sat"`
	AspectGamma   float64 `yaml:"aspect_gamma"`

	// —— 阶段 B 完整切面聚类（P1，docs/impl/v1/wiki-generation.md 2.1/2.2）——
	// AspectWIntent/AspectWUnit are the two edge signals P0's cohesion-only
	// PairSignals didn't need: verified-ActivationLink intent Jaccard (usage
	// condition similarity) and same-unit fallback (material-side, weakest).
	AspectWIntent float64 `yaml:"aspect_w_intent"`
	AspectWUnit   float64 `yaml:"aspect_w_unit"`
	// AspectSplitGammaFactor multiplies AspectGamma when an oversized
	// community is recursively re-clustered once (2.2 "后处理").
	AspectSplitGammaFactor float64 `yaml:"aspect_split_gamma_factor"`
	// AspectMinSize/AspectMaxSize bound a leaf aspect's point count after
	// Louvain; undersized communities merge into their strongest neighbor or
	// fall into the reserved "misc" bucket, oversized ones split once.
	AspectMinSize int `yaml:"aspect_min_size"`
	AspectMaxSize int `yaml:"aspect_max_size"`
	// AspectQuestionsMax caps how many real confident question strings ride
	// along per aspect into the analyze stage, and how many populate
	// PageAspect.QuestionTypes (<=0 defaults to 5).
	AspectQuestionsMax int `yaml:"aspect_questions_max"`
}

type StudyConfig struct {
	ScheduleInterval      string  `yaml:"schedule_interval"`
	CandidateConfidentMin int     `yaml:"candidate_confident_min"`
	CandidateRatioMin     float64 `yaml:"candidate_ratio_min"`
	WikiKPMin             int     `yaml:"wiki_kp_min"`
	GapHitThreshold       int     `yaml:"gap_hit_threshold"`
	ScanBatchSize         int     `yaml:"scan_batch_size"`
	ReportPeriodDays      int     `yaml:"report_period_days"`
	ReportMaxKeep         int     `yaml:"report_max_keep"`
	// —— V1 新增（docs/impl/v1/study.md 配置项）——
	AutoPromote        bool    `yaml:"auto_promote"`
	PromoteSuccessMin  int     `yaml:"promote_success_min"`
	PromoteDistinctMin int     `yaml:"promote_distinct_min"`
	WeakenFailureMin   int     `yaml:"weaken_failure_min"`
	WeakenRatioMin     float64 `yaml:"weaken_ratio_min"`
	ReverifySuccessMin int     `yaml:"reverify_success_min"`
	EventWindowDays    int     `yaml:"event_window_days"`
	CandidateIdleDays  int     `yaml:"candidate_idle_days"`
	DeprecateIdleDays  int     `yaml:"deprecate_idle_days"`
	CorrectionWeight   int     `yaml:"correction_weight"`
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
	// —— 问题复杂度观测量（两层架构扩展，docs/impl/v1/study.md 步骤 7）——
	ComplexityMinQuestions int `yaml:"complexity_min_questions"`
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
