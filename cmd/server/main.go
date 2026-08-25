package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jxman78/wiki-brain/internal/activation"
	"github.com/jxman78/wiki-brain/internal/answer"
	"github.com/jxman78/wiki-brain/internal/domain"
	"github.com/jxman78/wiki-brain/internal/entry"
	"github.com/jxman78/wiki-brain/internal/evidence"
	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/db"
	"github.com/jxman78/wiki-brain/internal/foundation/index"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/progress"
	"github.com/jxman78/wiki-brain/internal/foundation/queue"
	"github.com/jxman78/wiki-brain/internal/llmconfig"
	"github.com/jxman78/wiki-brain/internal/retrieval"
	"github.com/jxman78/wiki-brain/internal/session"
	"github.com/jxman78/wiki-brain/internal/source"
	"github.com/jxman78/wiki-brain/internal/study"
	"github.com/jxman78/wiki-brain/internal/trace"
	"github.com/jxman78/wiki-brain/internal/unit"
	"github.com/jxman78/wiki-brain/internal/wiki"
	"github.com/jxman78/wiki-brain/web"
)

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "", "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}

	baseDir, _ := os.Getwd()

	logOpts := foundation.LogOptions{
		Level:      cfg.Logging.ParseLevel(),
		Dir:        cfg.Logging.Dir,
		Filename:   cfg.Logging.Filename,
		Console:    cfg.Logging.Console,
		File:       cfg.Logging.File,
		MaxSizeMB:  cfg.Logging.MaxSizeMB,
		MaxBackups: cfg.Logging.MaxBackups,
		MaxAgeDays: cfg.Logging.MaxAgeDays,
		Compress:   cfg.Logging.Compress,
	}

	if _, err := foundation.InitLogger(logOpts); err != nil {
		fmt.Fprintf(os.Stderr, "初始化日志失败: %v\n", err)
		os.Exit(1)
	}

	accessLogOpts := logOpts
	accessLogOpts.Console = cfg.Logging.AccessConsole
	accessLogger, err := foundation.NewAccessLogger(accessLogOpts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化访问日志失败: %v\n", err)
		os.Exit(1)
	}

	if err := foundation.EnsureDirectories(baseDir); err != nil {
		slog.Error("创建目录失败", "error", err)
		os.Exit(1)
	}

	// gse 分词器：必须在任何触发 InitSegmenter() 的调用（trace/activation 的
	// question_terms 归一化、Bleve 索引）之前显式带上自定义词典完成首次加载
	// （sync.Once），否则会有其他调用点用零参数把词典锁定成只含基础词库，
	// 导致"达梦""会话"这类词典外术语被切碎成单字。
	if err := index.InitSegmenter("config/dict/it.txt", "config/dict/finance.txt", "config/dict/wiki_brain.txt"); err != nil {
		slog.Error("初始化分词器失败", "error", err)
		os.Exit(1)
	}

	// Database
	database, err := db.Open(cfg.Database.Path)
	if err != nil {
		slog.Error("打开数据库失败", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	// Preset data
	foundation.LoadPresetData(database, "preset/domains.json")

	// Bleve indexes — EnsureHealthy checks doc counts against the DB and
	// tokenizer searchability, rebuilding from scratch when they drift
	// (NewManager alone just opens whatever is on disk, even if stale/empty).
	idxMgr, err := index.EnsureHealthy(cfg.Index.Path, database, func(sourceID string) ([]string, error) {
		var mdPath string
		if err := database.QueryRow("SELECT markdown_path FROM sources WHERE source_id = ?", sourceID).Scan(&mdPath); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(mdPath)
		if err != nil {
			return nil, err
		}
		return strings.Split(string(data), "\n"), nil
	})
	if err != nil {
		slog.Error("初始化索引失败", "error", err)
		os.Exit(1)
	}
	defer idxMgr.Close()

	// Queue
	bufSize := cfg.Queue.BufferSize
	if bufSize <= 0 {
		bufSize = 100
	}
	q := queue.New(bufSize)

	// LLM client
	llmRouter := llm.NewRoutingClient("config/prompts")
	llmConfigStore := llmconfig.NewStore(database)
	llmConfigSvc := llmconfig.NewService(llmConfigStore, llmRouter)
	if err := llmConfigSvc.BootstrapFromYAML(cfg.BootstrapLLM); err != nil {
		slog.Error("LLM 配置引导失败", "error", err)
		os.Exit(1)
	}
	if err := llmConfigSvc.ReloadRouter(context.Background()); err != nil {
		slog.Error("加载 LLM 配置失败", "error", err)
		os.Exit(1)
	}
	var llmClient llm.LLMClient = llmRouter

	// FileView client
	fvClient := source.NewFileViewClient(
		cfg.FileView.BaseURL,
		cfg.FileView.PollIntervalMs,
		cfg.FileView.MaxPollSeconds,
	)

	// Progress broadcaster
	broadcaster := progress.NewBroadcaster()

	// ── Stores ──────────────────────────────────────────
	sourceStore := source.NewStore(database)

	// A source/units row left in "processing" only happens if the previous
	// run was killed mid-task (crash, forced restart) — on a clean run that
	// status is always terminal by the time the queue handler returns. Flag
	// those as failed now, before the queue starts accepting tasks, so the
	// file management page doesn't show them as stuck "处理中" forever with
	// nothing actually working on them; the existing retry/reupload-retry
	// endpoints pick up from "failed" normally.
	if srcN, unitsN, err := sourceStore.RecoverInterruptedProcessing(); err != nil {
		slog.Error("恢复中断处理状态失败", "error", err)
	} else if srcN > 0 || unitsN > 0 {
		slog.Warn("发现服务重启前中断的处理任务，已标记为失败", "source_count", srcN, "units_count", unitsN)
	}

	unitStore := unit.NewStore(database)
	retrievalStore := retrieval.NewStore(database)
	answerStore := answer.NewStore(database)
	traceStore := trace.NewStore(database)
	sessionStore := session.NewStore(database)
	studyStore := study.NewStore(database)
	activationStore := activation.NewStore(database)
	wikiStore := wiki.NewStore(database)
	entryStore := entry.NewStore(database)
	domainStore := domain.NewStore(database)

	// ── Services ────────────────────────────────────────
	sourceSvc := source.NewService(sourceStore, fvClient, llmClient, llmRouter, idxMgr.Outlines, q, cfg, baseDir)
	sourceSvc.SetBroadcaster(broadcaster)
	sourceSvc.SetUnitIndexes(idxMgr.Units, idxMgr.Points)

	unitSvc := unit.NewService(unitStore, sourceStore, llmClient, idxMgr.Units, idxMgr.Points, q, cfg)
	unitSvc.SetBroadcaster(broadcaster)
	sourceSvc.SetLifecycleSetter(unitSvc)
	sourceSvc.SetEntryMatcher(unitSvc)

	activationMatcher := activation.NewMatcher(activationStore)
	activationSvc := activation.NewService(activationStore, activationMatcher)
	unitSvc.SetActivationNotifier(activationSvc)
	activationSvc.SetConfidenceConfig(activation.ConfidenceConfig{
		ServingConfidenceMin:  cfg.Retrieval.ServingConfidenceMin,
		AuditSampleMin:        cfg.Retrieval.AuditSampleMin,
		ExploreRateLow:        cfg.Retrieval.ExploreRateLow,
		ExploreRateSelfGraded: cfg.Retrieval.ExploreRateSelfGraded,
		ExploreRateTrusted:    cfg.Retrieval.ExploreRateTrusted,
	})

	// 问题四元组归一化（2026-08-12 新增，docs/impl/v1/retrieval.md 步骤 2）：
	// 总是构造 TupleNormalizer 并挂上 LLM 客户端，是否实际生效由
	// cfg.Retrieval.QuestionTupleNormEnabled 在 Retrieval 侧门控（默认关闭）。
	tupleNormalizer := activation.NewTupleNormalizer(activationStore, activation.TupleNormConfig{
		LocalSimMin: cfg.Retrieval.QuestionTupleNormLocalSimMin,
	})
	tupleNormalizer.SetLLMClient(llmClient)
	activationSvc.SetTupleNormalizer(tupleNormalizer)

	evidenceSvc := evidence.NewService(llmClient, cfg.Evidence)

	wikiSvc := wiki.NewService(wikiStore, llmClient, idxMgr.Wiki, idxMgr.Points, idxMgr.Outlines, cfg.Wiki)
	wikiSvc.SetActivationSvc(activationSvc)
	// 2026-08-18 单层化收尾重新接线（docs/impl/v1/wiki.md「重编译标记」）：
	// KP lifecycle 变化、entry_id 归属变化通知 Wiki 标记 needs_recompile。
	unitSvc.SetWikiEntryNotifier(wikiSvc)

	retrievalSvc := retrieval.NewService(retrievalStore, llmClient, idxMgr.Units, idxMgr.Points, idxMgr.Outlines, cfg, activationSvc, evidenceSvc, wikiSvc)
	answerSvc := answer.NewService(answerStore, llmClient, q, retrievalSvc)
	traceSvc := trace.NewService(traceStore, cfg.Study.EntryNullRatioMin)
	traceSvc.SetLLMClient(llmClient)
	traceSvc.SetObservedConditionEnricher(activationSvc, cfg.Study.ObservedConditionsMax)
	traceSvc.SetCorrectionWeight(cfg.Study.CorrectionWeight)
	retrievalSvc.SetAuditOutcomeWriter(traceSvc)
	traceSvc.SetSynthesisOutcomeWriter(wikiSvc)
	retrievalSvc.SetSynthesisOutcomeWriter(traceSvc)
	traceSvc.SetSourceAffinityWriter(retrievalSvc)
	studySvc := study.NewService(studyStore, cfg.Study, activationSvc, wikiSvc, cfg.Wiki.RecompileNewKPMin, cfg.Wiki.QualifyingMinDaysActive,
		cfg.Retrieval.QuestionTupleNormIdleDays, cfg.Retrieval.QuestionTupleNormEnabled,
		cfg.Retrieval.RerankTopN, cfg.Retrieval.OutlineRRFBoost)
	studySvc.SetSourceAffinityCleanup(retrievalSvc, cfg.Retrieval.SourceAffinityIdleDays)

	entrySvc := entry.NewService(entryStore, entry.Config{
		AddEventMin:       cfg.Study.EntryAddEventMin,
		AddDistinctMin:    cfg.Study.EntryAddDistinctMin,
		AddOverlapMin:     cfg.Study.EntryAddOverlapMin,
		MergeCooccurMin:   cfg.Study.EntryMergeCooccurMin,
		MergeOverlapMin:   cfg.Study.EntryMergeOverlapMin,
		CandidateIdleDays: cfg.Study.EntryCandidateIdleDays,
		EventWindowDays:   cfg.Study.EntryEventWindowDays,
		AutoConfirmAdd:    cfg.Study.EntryAddAutoConfirm,
	}, wikiSvc)
	studySvc.SetEntrySvc(entrySvc)
	unitSvc.SetEntryNotifier(entrySvc)
	entrySvc.SetKPNRematchNotifier(unitSvc)
	domainSvc := domain.NewService(domainStore)

	// ── Queue handlers ──────────────────────────────────
	// source_process and unit_extract each get their own dedicated worker
	// pool sized by source.upload_concurrency, independent of each other and
	// of queue.workers below — at most that many sources can be running
	// source_process at once, and independently at most that many running
	// unit_extract at once.
	uploadConcurrency := cfg.Source.UploadConcurrency
	if uploadConcurrency <= 0 {
		uploadConcurrency = 2
	}

	q.RegisterHandlerWithWorkers(queue.TaskTypeSourceProcess, uploadConcurrency, func(payload interface{}) {
		task := payload.(queue.SourceTask)
		if err := sourceSvc.Process(context.Background(), task.SourceID); err != nil {
			slog.Error("source process failed", "source_id", task.SourceID, "error", err)
			broadcaster.Close(task.SourceID)
		}
	})

	q.RegisterHandlerWithWorkers(queue.TaskTypeUnitExtract, uploadConcurrency, func(payload interface{}) {
		task := payload.(queue.UnitTask)

		unitsStatus := "completed"
		if err := unitSvc.Extract(context.Background(), task.SourceID); errors.Is(err, unit.ErrExtractionInProgress) {
			// A concurrent run already owns this source's extraction and its
			// units_status — this duplicate task must not touch either.
			slog.Warn("unit extract skipped, already in progress", "source_id", task.SourceID)
			return
		} else if err != nil {
			slog.Error("unit extract failed", "source_id", task.SourceID, "error", err)
			unitsStatus = "failed"
		} else if err := sourceSvc.CompleteShadowSwap(context.Background(), task.SourceID); err != nil {
			// No-op when task.SourceID isn't a shadow; a real failure here means
			// task.SourceID *is* a shadow but the swap itself failed.
			if errors.Is(err, source.ErrShadowEmpty) {
				slog.Warn("shadow swap skipped: zero units extracted, target left untouched", "source_id", task.SourceID)
				unitsStatus = "failed"
			} else {
				slog.Error("shadow swap failed", "source_id", task.SourceID, "error", err)
			}
		}
		// If task.SourceID was a shadow that swapped successfully, this row is
		// already deleted by CompleteShadowSwap (which sets the target's own
		// units_status directly) — updating it here just affects zero rows.
		if err := sourceStore.UpdateUnitsStatus(task.SourceID, unitsStatus); err != nil {
			slog.Error("update units_status failed", "source_id", task.SourceID, "units_status", unitsStatus, "error", err)
		}

		broadcaster.Close(task.SourceID)
	})

	q.RegisterHandler(queue.TaskTypeTrace, func(payload interface{}) {
		task := payload.(*queue.TraceTask)
		traceSvc.ProcessTrace(task.Result.(*answer.AnswerResult))
	})

	queueWorkers := cfg.Queue.Workers
	if queueWorkers <= 0 {
		queueWorkers = 1
	}
	q.StartN(queueWorkers)

	// ── Study scheduler ─────────────────────────────────
	studyInterval, err := time.ParseDuration(cfg.Study.ScheduleInterval)
	if err != nil {
		studyInterval = 1 * time.Hour
	}
	studyScheduler := study.NewScheduler(studySvc, studyInterval)
	studyScheduler.Start()

	// ── HTTP routes ─────────────────────────────────────
	mux := foundation.NewRouter()
	prefix := strings.TrimRight(cfg.Server.PathPrefix, "/")

	// Web UI
	mux.HandleFunc("GET "+prefix+"/", func(w http.ResponseWriter, r *http.Request) {
		data, err := web.FS.ReadFile("index.html")
		if err != nil {
			http.Error(w, "page not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})
	mux.HandleFunc("GET "+prefix+"/marked.min.js", func(w http.ResponseWriter, r *http.Request) {
		data, err := web.FS.ReadFile("marked.min.js")
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Write(data)
	})

	// API routes — if prefix is set, wrap mux with StripPrefix
	apiMux := mux
	if prefix != "" {
		apiMux = foundation.NewRouter()
	}
	source.NewHandler(sourceSvc).RegisterRoutes(apiMux)
	unit.NewHandler(unitSvc).RegisterRoutes(apiMux)
	retrieval.NewHandler(retrievalSvc).RegisterRoutes(apiMux)
	answerHandler := answer.NewHandler(answerSvc)
	answerHandler.SetDB(database)
	answerHandler.RegisterRoutes(apiMux)
	trace.NewHandler(traceSvc).RegisterRoutes(apiMux)
	study.NewHandler(studySvc).RegisterRoutes(apiMux)
	sessionParser := session.NewParser(llmClient)
	sessionParser.SetDomainCatalog(retrievalDomainCatalog{store: retrievalStore})
	session.NewHandler(sessionStore, sessionParser).RegisterRoutes(apiMux)
	activation.NewHandler(activationSvc, unitStore, sourceStore).RegisterRoutes(apiMux)
	wiki.NewHandler(wikiSvc).RegisterRoutes(apiMux)
	entry.NewHandler(entrySvc).RegisterRoutes(apiMux)
	domain.NewHandler(domainSvc).RegisterRoutes(apiMux)
	llmconfig.NewHandler(llmConfigSvc).RegisterRoutes(apiMux)

	var rootHandler http.Handler = mux
	if prefix != "" {
		mux.Handle(prefix+"/", http.StripPrefix(prefix, apiMux))
		rootHandler = mux
	}

	// Middleware chain
	handler := corsMiddleware(
		foundation.Chain(rootHandler,
			foundation.RequestIDMiddleware,
			foundation.LoggingMiddleware(accessLogger),
		),
	)

	// Concurrency limiter
	if cfg.Server.MaxConcurrency > 0 {
		handler = concurrencyMiddleware(handler, cfg.Server.MaxConcurrency)
	}

	// ── Server ──────────────────────────────────────────
	host := cfg.Server.Host
	if host == "" {
		host = "0.0.0.0"
	}
	port := cfg.Server.Port
	if port <= 0 {
		port = 8080
	}

	readTimeout := parseDuration(cfg.Server.ReadTimeout, 30*time.Second)
	writeTimeout := parseDuration(cfg.Server.WriteTimeout, 60*time.Second)

	addr := fmt.Sprintf("%s:%d", host, port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
	}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		slog.Info("收到退出信号", "signal", sig)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		studyScheduler.Stop()
		q.Shutdown()

		if err := srv.Shutdown(ctx); err != nil {
			slog.Error("HTTP 服务关闭失败", "error", err)
		}
	}()

	slog.Info("知识大脑启动", "addr", addr, "prefix", prefix, "url", fmt.Sprintf("http://localhost:%d%s/", port, prefix))

	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("HTTP 服务异常退出", "error", err)
		os.Exit(1)
	}

	slog.Info("知识大脑已停止")
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Request-ID")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func concurrencyMiddleware(next http.Handler, max int) http.Handler {
	sem := make(chan struct{}, max)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
			next.ServeHTTP(w, r)
		default:
			http.Error(w, "server too busy", http.StatusServiceUnavailable)
		}
	})
}

func parseDuration(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}

// retrievalDomainCatalog adapts retrieval.Store.ListDomains for session.Parser
// merged parse+domain routing (avoids session→retrieval import cycle).
type retrievalDomainCatalog struct {
	store *retrieval.Store
}

func (c retrievalDomainCatalog) ListDomainEntries() ([]session.DomainEntry, error) {
	domains, err := c.store.ListDomains()
	if err != nil {
		return nil, err
	}
	out := make([]session.DomainEntry, 0, len(domains))
	for _, d := range domains {
		out = append(out, session.DomainEntry{
			ID:          d.DomainID,
			Name:        d.Name,
			Description: d.Description,
		})
	}
	return out, nil
}
