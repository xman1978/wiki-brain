package retrieval

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

// fakeSynthesisWriter is a mock SynthesisOutcomeWriter that signals a channel
// on each call — same synchronization pattern as fakeAuditWriter
// (docs/impl/v1/wiki.md 步骤 4a).
type fakeSynthesisWriter struct {
	mu    sync.Mutex
	calls []synthesisCall
	ch    chan synthesisCall
}

type synthesisCall struct {
	pageID, question       string
	slowPathDirectPointIDs []string
	agree                  bool
}

func newFakeSynthesisWriter() *fakeSynthesisWriter {
	return &fakeSynthesisWriter{ch: make(chan synthesisCall, 8)}
}

func (f *fakeSynthesisWriter) WriteSynthesisOutcome(pageID, question string, slowPathDirectPointIDs []string, agree bool) error {
	c := synthesisCall{pageID, question, slowPathDirectPointIDs, agree}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	f.ch <- c
	return nil
}

func (f *fakeSynthesisWriter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// TestSynthesisAuditTrial_SampledHit_RunsAndAgrees exercises
// launchSynthesisAuditTrial → runSynthesisAuditTrial → WriteSynthesisOutcome
// end to end: with the sampling RNG forced to always sample (randFloat
// returns 0 < any positive rate), a served Wiki direct answer must trigger
// an independent slow-path trial whose direct evidence (p1) intersects the
// page's source_point_ids (["p1"]), so agree=true.
func TestSynthesisAuditTrial_SampledHit_RunsAndAgrees(t *testing.T) {
	svc, fake, _ := setupTestServiceWithWiki(t)
	writer := newFakeSynthesisWriter()
	svc.SetSynthesisOutcomeWriter(writer)
	svc.cfg.Wiki.SynthesisAuditRate = 1.0
	svc.synthesisRandFloat = func() float64 { return 0 }
	setResponsesForFullSlowPathSuccess(fake)

	svc.launchSynthesisAuditTrial(context.Background(), QueryContext{Question: "linear equations"}, "w1")

	select {
	case call := <-writer.ch:
		if call.pageID != "w1" {
			t.Fatalf("unexpected call: %+v", call)
		}
		if !call.agree {
			t.Fatalf("expected agree=true (p1 in both page scope and slow path direct evidence), got false; slowPathDirectPointIDs=%v", call.slowPathDirectPointIDs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for synthesis audit trial to call WriteSynthesisOutcome")
	}
}

// TestSynthesisAuditTrial_NotSampled_NeverTriggers asserts that when the
// sampling RNG returns a value at/above the configured rate, no trial fires
// at all — the boundary condition from docs/impl/v1/wiki.md 步骤 4a's「未中
// 选 → 不产生任何 synthesis 事件」.
func TestSynthesisAuditTrial_NotSampled_NeverTriggers(t *testing.T) {
	svc, fake, _ := setupTestServiceWithWiki(t)
	writer := newFakeSynthesisWriter()
	svc.SetSynthesisOutcomeWriter(writer)
	svc.cfg.Wiki.SynthesisAuditRate = 0.1
	svc.synthesisRandFloat = func() float64 { return 0.1 } // exactly at the rate boundary: not sampled (randFloat() >= rate)
	setResponsesForFullSlowPathSuccess(fake)

	svc.launchSynthesisAuditTrial(context.Background(), QueryContext{Question: "linear equations"}, "w1")

	select {
	case call := <-writer.ch:
		t.Fatalf("expected no synthesis audit trial call, got %+v", call)
	case <-time.After(300 * time.Millisecond):
		// expected: nothing arrives
	}
	if writer.callCount() != 0 {
		t.Fatalf("callCount = %d, want 0", writer.callCount())
	}
}

// TestSynthesisAuditTrial_ZeroRate_NeverTriggers covers the disabled-by-
// default posture: rate<=0 must never sample regardless of randFloat.
func TestSynthesisAuditTrial_ZeroRate_NeverTriggers(t *testing.T) {
	svc, _, _ := setupTestServiceWithWiki(t)
	writer := newFakeSynthesisWriter()
	svc.SetSynthesisOutcomeWriter(writer)
	svc.cfg.Wiki.SynthesisAuditRate = 0
	svc.synthesisRandFloat = func() float64 { return 0 }

	svc.launchSynthesisAuditTrial(context.Background(), QueryContext{Question: "linear equations"}, "w1")

	select {
	case call := <-writer.ch:
		t.Fatalf("expected no synthesis audit trial call at rate=0, got %+v", call)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestSynthesisAuditTrial_NoWriterConfigured_NeverTriggers asserts the
// nil-safe optionality (mirrors AuditOutcomeWriter's): with no writer wired,
// launchSynthesisAuditTrial must not panic or spawn a goroutine.
func TestSynthesisAuditTrial_NoWriterConfigured_NeverTriggers(t *testing.T) {
	svc, _, _ := setupTestServiceWithWiki(t)
	svc.cfg.Wiki.SynthesisAuditRate = 1.0
	svc.synthesisRandFloat = func() float64 { return 0 }

	// Must not panic.
	svc.launchSynthesisAuditTrial(context.Background(), QueryContext{Question: "linear equations"}, "w1")
	time.Sleep(50 * time.Millisecond)
}

// TestSynthesisAuditTrial_ViaFullWikiDirectAnswer_Fires exercises the actual
// call site inside tryWikiAnswer — a successfully served Wiki direct answer
// triggers the sampled trial without delaying the response.
func TestSynthesisAuditTrial_ViaFullWikiDirectAnswer_Fires(t *testing.T) {
	svc, fake, _ := setupTestServiceWithWiki(t)
	writer := newFakeSynthesisWriter()
	svc.SetSynthesisOutcomeWriter(writer)
	svc.cfg.Wiki.SynthesisAuditRate = 1.0
	svc.synthesisRandFloat = func() float64 { return 0 }
	fake.SetResponse("answer_wiki.md", llm.FakeResponse{Output: `{"content":"回答：ax+b=0 是线性方程 [p1]","citations":["p1"],"coverage":"full"}`})
	setResponsesForFullSlowPathSuccess(fake)

	es, err := svc.RetrieveWithProgress(context.Background(), QueryContext{Question: "linear equations"}, nil)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if es.PathType != PathTypeWiki {
		t.Fatalf("path_type = %q, want wiki", es.PathType)
	}

	select {
	case call := <-writer.ch:
		if call.pageID != "w1" {
			t.Errorf("unexpected page_id: %+v", call)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for synthesis audit trial triggered from a real Wiki direct answer")
	}
}
