package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	LLM       LLMConfig       `yaml:"llm"`
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
}

type LLMConfig struct {
	BaseURL        string                 `yaml:"base_url"`
	APIKey         string                 `yaml:"api_key"`
	TimeoutSeconds int                    `yaml:"timeout_seconds"`
	MaxRetries     int                    `yaml:"max_retries"`
	Models         map[string]ModelConfig `yaml:"models"`
}

type ModelConfig struct {
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
}

// EvidenceConfig — docs/impl/v1/evidence.md 配置项.
type EvidenceConfig struct {
	Enabled           bool `yaml:"enabled"`
	BatchMaxChars     int  `yaml:"batch_max_chars"`
	MaxFragmentsPerKU int  `yaml:"max_fragments_per_ku"`
	MinFragmentChars  int  `yaml:"min_fragment_chars"`
	Retry             int  `yaml:"retry"`
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
}

type StudyConfig struct {
	ScheduleInterval      string  `yaml:"schedule_interval"`
	CandidateConfidentMin int     `yaml:"candidate_confident_min"`
	CandidateRatioMin     float64 `yaml:"candidate_ratio_min"`
	WikiKPMin             int     `yaml:"wiki_kp_min"`
	WikiConfidentMin      int     `yaml:"wiki_confident_min"`
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
	ConceptNullRatioMin      float64 `yaml:"concept_null_ratio_min"`
	ConceptAddEventMin       int     `yaml:"concept_add_event_min"`
	ConceptAddDistinctMin    int     `yaml:"concept_add_distinct_min"`
	ConceptAddOverlapMin     float64 `yaml:"concept_add_overlap_min"`
	ConceptMergeCooccurMin   int     `yaml:"concept_merge_cooccur_min"`
	ConceptMergeOverlapMin   float64 `yaml:"concept_merge_overlap_min"`
	ConceptCandidateIdleDays int     `yaml:"concept_candidate_idle_days"`
	ConceptEventWindowDays   int     `yaml:"concept_event_window_days"`
}

func (c *LLMConfig) TimeoutDuration() time.Duration {
	if c.TimeoutSeconds > 0 {
		return time.Duration(c.TimeoutSeconds) * time.Second
	}
	return 120 * time.Second
}

func (c *LLMConfig) ModelForPurpose(purpose string) ModelConfig {
	if m, ok := c.Models[purpose]; ok {
		return m
	}
	if m, ok := c.Models["default"]; ok {
		return m
	}
	return ModelConfig{}
}

func (c *LLMConfig) ResolvedAPIKey() string {
	if val := os.Getenv(c.APIKey); val != "" {
		return val
	}
	return c.APIKey
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
		"WB_LLM_BASE_URL":         &cfg.LLM.BaseURL,
		"WB_LLM_API_KEY":          &cfg.LLM.APIKey,
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
		"WB_LLM_TIMEOUT_SECONDS":    &cfg.LLM.TimeoutSeconds,
		"WB_LLM_MAX_RETRIES":        &cfg.LLM.MaxRetries,
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
