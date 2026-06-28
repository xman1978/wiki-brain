package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"os/signal"
	"syscall"
	"time"

	"github.com/jxman78/wiki-brain/internal/answer"
	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/db"
	"github.com/jxman78/wiki-brain/internal/foundation/index"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/progress"
	"github.com/jxman78/wiki-brain/internal/foundation/queue"
	"github.com/jxman78/wiki-brain/internal/retrieval"
	"github.com/jxman78/wiki-brain/internal/session"
	"github.com/jxman78/wiki-brain/internal/source"
	"github.com/jxman78/wiki-brain/internal/study"
	"github.com/jxman78/wiki-brain/internal/trace"
	"github.com/jxman78/wiki-brain/internal/unit"
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

	if _, err := foundation.InitLogger("logs", slog.LevelInfo); err != nil {
		fmt.Fprintf(os.Stderr, "初始化日志失败: %v\n", err)
		os.Exit(1)
	}

	if err := foundation.EnsureDirectories(baseDir); err != nil {
		slog.Error("创建目录失败", "error", err)
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

	// Bleve indexes
	idxMgr, err := index.NewManager(cfg.Index.Path)
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
	llmClient, err := llm.NewOpenAIClient(&cfg.LLM, "config/prompts")
	if err != nil {
		slog.Error("初始化 LLM 客户端失败", "error", err)
		os.Exit(1)
	}

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
	unitStore := unit.NewStore(database)
	retrievalStore := retrieval.NewStore(database)
	answerStore := answer.NewStore(database)
	traceStore := trace.NewStore(database)
	sessionStore := session.NewStore(database)
	studyStore := study.NewStore(database)

	// ── Services ────────────────────────────────────────
	sourceSvc := source.NewService(sourceStore, fvClient, llmClient, idxMgr.Outlines, q, cfg, baseDir)
	sourceSvc.SetBroadcaster(broadcaster)
	sourceSvc.SetUnitIndexes(idxMgr.Units, idxMgr.Points)

	unitSvc := unit.NewService(unitStore, sourceStore, llmClient, idxMgr.Units, idxMgr.Points, q, cfg)
	unitSvc.SetBroadcaster(broadcaster)

	retrievalSvc := retrieval.NewService(retrievalStore, llmClient, idxMgr.Units, idxMgr.Points, idxMgr.Outlines, cfg)
	answerSvc := answer.NewService(answerStore, llmClient, q, retrievalSvc)
	traceSvc := trace.NewService(traceStore)
	studySvc := study.NewService(studyStore, cfg.Study)

	// ── Queue handlers ──────────────────────────────────
	q.RegisterHandler(queue.TaskTypeSourceProcess, func(payload interface{}) {
		task := payload.(queue.SourceTask)
		if err := sourceSvc.Process(context.Background(), task.SourceID); err != nil {
			slog.Error("source process failed", "source_id", task.SourceID, "error", err)
		}
		broadcaster.Close(task.SourceID)
	})

	q.RegisterHandler(queue.TaskTypeUnitExtract, func(payload interface{}) {
		task := payload.(queue.UnitTask)
		if err := unitSvc.Extract(context.Background(), task.SourceID); err != nil {
			slog.Error("unit extract failed", "source_id", task.SourceID, "error", err)
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
	session.NewHandler(sessionStore, session.NewParser(llmClient)).RegisterRoutes(apiMux)

	var rootHandler http.Handler = mux
	if prefix != "" {
		mux.Handle(prefix+"/", http.StripPrefix(prefix, apiMux))
		rootHandler = mux
	}

	// Middleware chain
	handler := corsMiddleware(
		foundation.Chain(rootHandler,
			foundation.RequestIDMiddleware,
			foundation.LoggingMiddleware,
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
