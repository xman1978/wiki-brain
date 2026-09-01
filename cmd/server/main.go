package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io/fs"
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
	"github.com/jxman78/wiki-brain/internal/mcp"
	"github.com/jxman78/wiki-brain/internal/retrieval"
	"github.com/jxman78/wiki-brain/internal/session"
	"github.com/jxman78/wiki-brain/internal/source"
	"github.com/jxman78/wiki-brain/internal/study"
	"github.com/jxman78/wiki-brain/internal/sysconfig"
	"github.com/jxman78/wiki-brain/internal/trace"
	"github.com/jxman78/wiki-brain/internal/unit"
	"github.com/jxman78/wiki-brain/internal/wiki"
	"github.com/jxman78/wiki-brain/web"
)

// helpManualFileName is the Source file_name identity used for the
// auto-synced 使用手册 (web/manual.md) — keep it stable so re-syncs always
// find the same Source rather than creating duplicates.
const helpManualFileName = "Wiki-Brain系统使用手册.md"

// helpManualSyncTimeout bounds how long maybeSyncHelpManual waits for the
// manual's Source to actually finish processing (register → convert →
// extract) before giving up on this round. A bad LLM config (wrong api_key,
// unreachable base_url, wrong model name, ...) shows up here as the Source
// landing in a failed state, not as an error from the initial Import/
// ImportShadow call — those only fail on obviously-bad input (unsupported
// format, duplicate name), not on downstream LLM failures, since the actual
// extraction runs asynchronously on the task queue.
const helpManualSyncTimeout = 10 * time.Minute

// maybeSyncHelpManual imports (or, if it already exists, reuploads) the
// built-in 使用手册 (web/manual.md) as a normal Source once the LLM is fully
// configured (all internal/llmconfig.PurposeList purposes bound), so it
// becomes answerable through 问答 like any other document — this is what
// lets the system explain its own usage when asked. A content hash persisted
// via sysconfig makes this idempotent (safe to call repeatedly) and lets a
// future edit to manual.md self-heal into the running system on the next
// call instead of requiring a manual reupload every time the docs change.
//
// The hash is only persisted after polling confirms the Source actually
// finished processing successfully (see waitForHelpManualSync) — not merely
// after Import/ImportShadow accepts the file. This matters because a wrong
// api_key/base_url/model lets the file register and queue just fine; it
// only surfaces as a failure once extraction actually calls the LLM. If we
// marked "synced" the moment the file was accepted, a bad model config
// would silently leave the manual permanently unimportable — the failure
// would only be visible if someone happened to open 文件 面板, and no future
// trigger would ever retry it (the content hash wouldn't have changed).
// Leaving the hash unset on failure means the very next trigger — the admin
// fixing the provider's api_key/base_url (SetOnConfigChanged also fires on
// CreateProvider/UpdateProvider, not just SetBindings) or the next server
// restart — retries automatically, with no admin action beyond fixing the
// config itself required. The 文件 panel's existing manual "重试" button
// also still works meanwhile as an immediate, visible fallback.
//
// Called once at startup and again after every successful LLM config change
// (see llmConfigSvc.SetOnConfigChanged below) so "no LLM configured yet" and
// "LLM configured but broken" both naturally resolve themselves once the
// admin finishes/fixes setup.
func maybeSyncHelpManual(ctx context.Context, llmConfigSvc *llmconfig.Service, sysConfigSvc *sysconfig.Service, sourceSvc *source.Service) {
	bindings, err := llmConfigSvc.GetBindings()
	if err != nil {
		slog.Error("使用手册自动同步：读取模型绑定失败", "error", err)
		return
	}
	for _, purpose := range llmconfig.PurposeList {
		if _, ok := bindings[purpose]; !ok {
			return // 大模型尚未完成配置，跳过——等下一次配置变更或下次启动再检查
		}
	}

	data, err := web.FS.ReadFile("manual.md")
	if err != nil {
		slog.Error("使用手册自动同步：读取内嵌文件失败", "error", err)
		return
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])

	lastHash, err := sysConfigSvc.GetHelpManualHash()
	if err != nil {
		slog.Error("使用手册自动同步：读取已同步版本记录失败", "error", err)
		return
	}
	if lastHash == hash {
		return // 内容未变化，已是最新（含此前已确认成功同步的情况）
	}

	existing, err := findSourceByFileName(sourceSvc, helpManualFileName)
	if err != nil {
		slog.Error("使用手册自动同步：查询已有文件失败", "error", err)
		return
	}

	var watchID string
	if existing == nil {
		created, err := sourceSvc.Import(ctx, helpManualFileName, bytes.NewReader(data))
		if err != nil {
			slog.Error("使用手册自动同步：导入失败", "error", err)
			return
		}
		watchID = created.SourceID
	} else {
		// 内容有更新，或此前一次导入曾经失败：走与手动"重新上传"完全相同的
		// Shadow Source 换血流程，不直接覆盖，处理失败也不影响原有可查询内容
		// （首次导入若失败，existing 就是那条 units_status=failed 的记录本身，
		// 这里等效于自动帮它按一次"重试"）。
		shadow, err := sourceSvc.ImportShadow(ctx, existing.SourceID, helpManualFileName, bytes.NewReader(data))
		if err != nil {
			slog.Error("使用手册自动同步：导入失败", "error", err)
			return
		}
		watchID = shadow.SourceID
	}

	if !waitForHelpManualSync(sourceSvc, watchID, existing != nil, helpManualSyncTimeout) {
		slog.Warn("使用手册自动同步：处理未成功完成，本轮放弃——请检查大模型配置是否可用（API Key、服务地址、模型名是否正确），也可以在「文件」面板里查看具体错误并手动重试；配置修复后下次保存系统设置或重启服务会自动重新尝试")
		return
	}

	if err := sysConfigSvc.SetHelpManualHash(hash); err != nil {
		slog.Error("使用手册自动同步：记录同步版本失败", "error", err)
		return
	}
	slog.Info("使用手册已自动同步为可问答的知识来源")
}

// waitForHelpManualSync polls until the just-triggered import/reupload of
// the 使用手册 either succeeds or definitively fails, so maybeSyncHelpManual
// only marks the content hash as synced on confirmed success. isReupload
// distinguishes the two outcome shapes: a fresh import's own Source row
// transitions pending/processing → completed/failed in place; a reupload's
// Shadow Source row instead disappears entirely on success (swapped into
// the target and deleted, docs/impl/v1/lifecycle.md 步骤 2) while staying
// present with units_status=failed on failure (so 系统设置里修复配置后，
// POST /sources/:id/reupload/retry 或本函数的下一次调用能找到它续跑).
func waitForHelpManualSync(sourceSvc *source.Service, sourceID string, isReupload bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		src, err := sourceSvc.Store().GetByID(sourceID)
		if err != nil {
			if isReupload && errors.Is(err, sql.ErrNoRows) {
				return true // 影子行已消失 = 换血成功，目标 Source 已接管新内容
			}
			return false
		}
		if src.Status == "failed" || src.UnitsStatus == "failed" {
			return false
		}
		if !isReupload && src.Status == "completed" && src.UnitsStatus == "completed" {
			return true
		}
		time.Sleep(3 * time.Second)
	}
	return false
}

// findSourceByFileName looks up a non-shadow Source by its exact file_name.
// There's no dedicated store method for this (only ExistsByFileName), so we
// reuse the filename LIKE-search behind List and filter for an exact match.
func findSourceByFileName(sourceSvc *source.Service, fileName string) (*source.Source, error) {
	sources, err := sourceSvc.Store().List("", "", fileName, 5, 0)
	if err != nil {
		return nil, err
	}
	for i := range sources {
		if sources[i].FileName == fileName {
			return &sources[i], nil
		}
	}
	return nil, nil
}

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

	// 系统设置（文件转换服务、历史会话）——DB-backed，页面即时生效，见
	// internal/sysconfig。
	sysConfigStore := sysconfig.NewStore(database)
	sysConfigSvc := sysconfig.NewService(sysConfigStore)

	// FileView client — DynamicFileViewClient 按当前设置在远程 FileView 服务
	// 与内置纯 Go 转换降级方案（docs/impl/v1/local-file-convert.md）之间选择，
	// 每次调用读取最新设置，系统设置页保存后无需重启即可生效。
	fileViewSettings, err := sysConfigSvc.GetFileView()
	if err != nil {
		slog.Error("读取文件转换服务设置失败", "error", err)
		os.Exit(1)
	}
	dynamicFVClient := sysconfig.NewDynamicFileViewClient(llmClient, fileViewSettings)
	sysConfigSvc.SetFileViewClient(dynamicFVClient)
	var fvClient source.FileViewClient = dynamicFVClient

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

	// 使用手册自学习接入：LLM 首次配置完成、或供应商/绑定后续任何变更
	// （含修复此前导致导入失败的错误配置）时自动把内置使用手册同步为可问答
	// 的 Source；同时在启动时也检查一次，覆盖"手册内容随版本更新"与"上次
	// 同步时模型还没配好/配错了"这几种场景。
	llmConfigSvc.SetOnConfigChanged(func() {
		go maybeSyncHelpManual(context.Background(), llmConfigSvc, sysConfigSvc, sourceSvc)
	})
	go maybeSyncHelpManual(context.Background(), llmConfigSvc, sysConfigSvc, sourceSvc)

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

	retrievalSvc := retrieval.NewService(retrievalStore, llmClient, idxMgr.Units, idxMgr.Points, cfg, activationSvc, evidenceSvc, wikiSvc)
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

		// Resolve the id that source_affinity backfill should tag *before*
		// CompleteShadowSwap runs below — a successful swap deletes
		// task.SourceID's row (it was the shadow), leaving only the target id
		// alive. Not a shadow: resolvedSourceID is just task.SourceID itself.
		resolvedSourceID := task.SourceID
		if shadow, err := sourceStore.GetByID(task.SourceID); err == nil && shadow.ShadowOf.Valid {
			resolvedSourceID = shadow.ShadowOf.String
		}

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

		if unitsStatus == "completed" {
			// Best-effort proactive re-tagging (docs/design/retrieval.md 第 14
			// 节 决策点 4): fire-and-forget, mirrors the existing
			// conceptMatcher.MatchEntries precedent in source/service.go — not
			// worth a 4th queue task type for a step whose only failure mode is
			// "this source misses a cache prewarm," never a correctness issue.
			go func(sourceID string) {
				if err := retrievalSvc.BackfillSourceAffinityForSource(context.Background(), sourceID); err != nil {
					slog.Warn("source affinity backfill failed", "source_id", sourceID, "error", err)
				}
			}(resolvedSourceID)
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

	// ── Session retention scheduler ─────────────────────
	sessionSettings, err := sysConfigSvc.GetSession()
	if err != nil {
		slog.Error("读取历史会话设置失败", "error", err)
		os.Exit(1)
	}
	sessionScheduler := session.NewScheduler(sessionStore, sessionSettings.RetentionDays, sessionSettings.Duration())
	sysConfigSvc.SetSessionScheduler(sessionScheduler)
	sessionScheduler.Start()

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
	// 用户使用手册：帮助图标打开的静态页面（help.html 用相对路径请求 manual.md
	// 并用已内嵌的 marked.min.js 客户端渲染，不走数据库/API）。
	mux.HandleFunc("GET "+prefix+"/help", func(w http.ResponseWriter, r *http.Request) {
		data, err := web.FS.ReadFile("help.html")
		if err != nil {
			http.Error(w, "page not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})
	mux.HandleFunc("GET "+prefix+"/manual.md", func(w http.ResponseWriter, r *http.Request) {
		data, err := web.FS.ReadFile("manual.md")
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Write(data)
	})
	// Vendored file-viewer widget (DOCX/PPTX/XLSX/PDF client-side rendering
	// for fileview.mode=local previews, see docs/impl/v1/local-file-convert.md
	// 第 8 节) — static files, served straight from the embedded FS.
	vendorFS, err := fs.Sub(web.FS, "vendor/file-viewer")
	if err != nil {
		fmt.Fprintf(os.Stderr, "挂载 vendor/file-viewer 失败: %v\n", err)
		os.Exit(1)
	}
	mux.Handle("GET "+prefix+"/vendor/file-viewer/", http.StripPrefix(prefix+"/vendor/file-viewer/", http.FileServer(http.FS(vendorFS))))

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
	sysconfig.NewHandler(sysConfigSvc).RegisterRoutes(apiMux)

	// MCP（对接 AI Agent 平台，docs/impl/v1/mcp.md）：与 REST API 共用同一个
	// service 图与数据库连接，走 Streamable HTTP，不是独立 stdio 子进程。
	mcpServer := mcp.NewServer(sourceSvc, sourceStore, unitStore, retrievalSvc, mcp.Config{
		ImportWaitTimeout:  time.Duration(cfg.Mcp.ImportWaitTimeoutSeconds) * time.Second,
		ImportPollInterval: time.Duration(cfg.Mcp.ImportPollIntervalMs) * time.Millisecond,
	})
	// 显式按方法注册（而不是裸路径 "/mcp"）：apiMux 在未设置 server.path_prefix
	// 时与 mux 是同一个 *http.ServeMux，裸路径模式匹配所有方法，会与已注册的
	// "GET /"（Web UI，带斜杠的子树通配）产生 Go 1.22+ ServeMux 无法判定优先级
	// 的冲突而在启动时 panic。Streamable HTTP 需要 POST（客户端消息）、GET
	// （服务端 SSE 流）、DELETE（会话终止）三种方法。
	mcpHandler := mcpServer.Handler()
	apiMux.Handle("POST /mcp", mcpHandler)
	apiMux.Handle("GET /mcp", mcpHandler)
	apiMux.Handle("DELETE /mcp", mcpHandler)

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
		sessionScheduler.Stop()
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
