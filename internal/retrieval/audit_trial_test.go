package retrieval

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/text"
)

// fakeAuditWriter is a mock AuditOutcomeWriter that signals a channel on each
// call — used instead of a flaky time.Sleep to synchronize on the background
// audit-trial goroutine (docs/impl/v1/retrieval.md 步骤 2c).
type fakeAuditWriter struct {
	mu    sync.Mutex
	calls []auditCall
	ch    chan auditCall
}

type auditCall struct {
	linkID, pointID, subject, intent, audience, constraint string
	agree                                                  bool
	slowPathDirectPointIDs                                 []string
}

func newFakeAuditWriter() *fakeAuditWriter {
	return &fakeAuditWriter{ch: make(chan auditCall, 8)}
}

func (f *fakeAuditWriter) WriteAuditOutcome(linkID, pointID, subject, intent, audience, constraint string, agree bool, slowPathDirectPointIDs []string) error {
	c := auditCall{linkID, pointID, subject, intent, audience, constraint, agree, slowPathDirectPointIDs}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	f.ch <- c
	return nil
}

func (f *fakeAuditWriter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// setResponsesForFullSlowPathSuccess configures the fake LLM client so a
// forced slow-path retrieval (RetrieveSlowPathWithProgress) against the
// setupTestService fixture (s1/u1/p1 ... d1) succeeds and returns p1 as
// direct evidence — mirrors TestRetrieveEndToEnd's setup.
func setResponsesForFullSlowPathSuccess(fake *llm.FakeClient) {
	fake.SetResponse("question_domain_match.md", llm.FakeResponse{Output: `{"domain_ids": ["d1"]}`})
	fake.SetResponse("source_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "s1", "relevant": true, "analysis": "match"}, {"candidate_id": "s3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("outline_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "o1", "relevant": false, "analysis": "no match"}, {"candidate_id": "o2", "relevant": true, "analysis": "match"}, {"candidate_id": "o3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("rerank_relevance.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "relevant": true, "analysis": "matches"}]}`})
	fake.SetResponse("rerank_classify.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "role": "direct", "analysis": "证据说明线性方程定义，可直接回答"}]}`})
}

// TestAuditTrial_SampledHit_RunsAndAgrees exercises the full launchAuditTrials
// → runAuditTrial → auditWriter.WriteAuditOutcome path for a hit with
// AuditSampled=true, and asserts agreement is computed correctly: the
// fast-path hit's point_id (p1) does appear among the slow path's
// independently-derived DirectEvidence point_ids, so agree=true.
func TestAuditTrial_SampledHit_RunsAndAgrees(t *testing.T) {
	svc, fake, _, _ := setupTestServiceWithActivation(t)
	writer := newFakeAuditWriter()
	svc.SetAuditOutcomeWriter(writer)
	setResponsesForFullSlowPathSuccess(fake)

	qc := QueryContext{Question: "linear equations"}
	hit := ActivationHit{
		LinkID: "link-1", PointID: "p1", Tier: "trusted", AuditSampled: true,
		Subject: "线性方程", Intent: "定义", Audience: "学生", Constraint: "",
	}

	svc.launchAuditTrials(qc, []ActivationHit{hit})

	select {
	case call := <-writer.ch:
		if call.linkID != "link-1" || call.pointID != "p1" {
			t.Fatalf("unexpected call: %+v", call)
		}
		if !call.agree {
			t.Fatalf("expected agree=true (p1 present in slow path direct point ids), got false; slowPathDirectPointIDs=%v", call.slowPathDirectPointIDs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for audit trial to call WriteAuditOutcome")
	}
}

// TestAuditTrial_NotSampled_NeverTriggers asserts hits with AuditSampled=false
// never launch a background trial — the mock writer must never be called.
func TestAuditTrial_NotSampled_NeverTriggers(t *testing.T) {
	svc, fake, _, _ := setupTestServiceWithActivation(t)
	writer := newFakeAuditWriter()
	svc.SetAuditOutcomeWriter(writer)
	setResponsesForFullSlowPathSuccess(fake)

	qc := QueryContext{Question: "linear equations"}
	hit := ActivationHit{LinkID: "link-1", PointID: "p1", Tier: "exploring", AuditSampled: false}

	svc.launchAuditTrials(qc, []ActivationHit{hit})

	select {
	case call := <-writer.ch:
		t.Fatalf("expected no audit trial call, got %+v", call)
	case <-time.After(300 * time.Millisecond):
		// expected: nothing arrives
	}
	if writer.callCount() != 0 {
		t.Fatalf("callCount = %d, want 0", writer.callCount())
	}
}

// TestAuditTrial_SlowPathFailure_NoEventWritten asserts a background slow-path
// failure never calls WriteAuditOutcome — no partial/error event, per the
// doc's "宁可少一次审计样本" rule. A nonexistent LLM prompt response (no
// FakeResponse configured) makes CompleteJSON error out.
func TestAuditTrial_SlowPathFailure_NoEventWritten(t *testing.T) {
	svc, fake, _, _ := setupTestServiceWithActivation(t)
	writer := newFakeAuditWriter()
	svc.SetAuditOutcomeWriter(writer)
	// Same domain/source/outline setup as TestRetrieveEndToEnd so recall
	// actually surfaces candidates (merged != empty), but deliberately leave
	// rerank_relevance.md unconfigured — FakeClient returns a hard "no
	// response configured" error there, which propagates all the way up
	// through RetrieveSlowPathWithProgress as a genuine "slow path itself
	// failed" case (as opposed to "slow path succeeded but found nothing",
	// which is a legitimate disagreement, not a dropped sample). Per 步骤
	// 2c: no WriteAuditOutcome call, no partial event, only a warn log.
	fake.SetResponse("question_domain_match.md", llm.FakeResponse{Output: `{"domain_ids": ["d1"]}`})
	fake.SetResponse("source_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "s1", "relevant": true, "analysis": "match"}, {"candidate_id": "s3", "relevant": false, "analysis": "no match"}]}`})
	fake.SetResponse("outline_filter.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "o1", "relevant": false, "analysis": "no match"}, {"candidate_id": "o2", "relevant": true, "analysis": "match"}, {"candidate_id": "o3", "relevant": false, "analysis": "no match"}]}`})

	qc := QueryContext{Question: "linear equations"}
	hit := ActivationHit{LinkID: "link-1", PointID: "p1", Tier: "trusted", AuditSampled: true}

	svc.launchAuditTrials(qc, []ActivationHit{hit})

	select {
	case call := <-writer.ch:
		t.Fatalf("expected no WriteAuditOutcome call on slow path failure, got %+v", call)
	case <-time.After(3 * time.Second):
		// expected: the goroutine logged a warning and returned without
		// calling the writer.
	}
	if writer.callCount() != 0 {
		t.Fatalf("callCount = %d, want 0", writer.callCount())
	}
}

// TestAuditTrial_MainRequestUnaffected asserts a fast-path response returns
// normally and promptly even when AuditSampled=true hits are present —
// the audit trial must not add latency to the original request.
func TestAuditTrial_MainRequestUnaffected(t *testing.T) {
	svc, fake, _, activationSvc := setupTestServiceWithActivation(t)
	writer := newFakeAuditWriter()
	svc.SetAuditOutcomeWriter(writer)

	question := "什么是线性方程"
	qTerms := text.Terms(text.Normalize(question))
	seedVerifiedLink(t, activationSvc, qTerms, "p1")
	fake.SetResponse("rerank_relevance.md", llm.FakeResponse{Output: `{"results": [{"candidate_id": "c1", "relevant": true, "analysis": "kpn neighbor"}, {"candidate_id": "c2", "relevant": true, "analysis": "kpn neighbor"}]}`})
	// Deliberately do NOT configure slow-path fake responses used by the
	// audit trial (question_domain_match.md etc.) — if the background trial
	// were somehow blocking the request, this would surface as a hang/error
	// on the main call; it must not.

	start := time.Now()
	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{Question: question}, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if es.PathType != PathTypeFast {
		t.Fatalf("path_type = %q, want fast", es.PathType)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("main request took %v, expected it to return promptly regardless of background audit trial", elapsed)
	}
}
