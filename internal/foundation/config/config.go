package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	LLM       LLMConfig       `yaml:"llm"`
	Server    ServerConfig    `yaml:"server"`
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
}

type RetrievalConfig struct {
	OutlineFTSMinScore         float64 `yaml:"outline_fts_min_score"`
	RerankTopN                 int     `yaml:"rerank_top_n"`
	ActivationMatchMin         float64 `yaml:"activation_match_min"`
	ActivationMatchMinFallback float64 `yaml:"activation_match_min_fallback"`
	ActivationMatchTop         int     `yaml:"activation_match_top"`
	// —— V1 新增（docs/impl/v1/retrieval.md 配置项）——
	FastPath         bool    `yaml:"fast_path"`
	FastPathFallback bool    `yaml:"fast_path_fallback"`
	WikiMinScore     float64 `yaml:"wiki_min_score"`
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

	applyEnvOverrides(&cfg)

	return &cfg, nil
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
	}

	for env, ptr := range intOverrides {
		if val := os.Getenv(env); val != "" {
			var n int
			if _, err := fmt.Sscanf(val, "%d", &n); err == nil {
				*ptr = n
			}
		}
	}

	_ = strings.Contains // ensure import used
}
