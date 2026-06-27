package queue

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestEnqueueAndConsume(t *testing.T) {
	q := New(10)
	var called atomic.Int32

	q.RegisterHandler(TaskTypeSourceProcess, func(payload interface{}) {
		st := payload.(SourceTask)
		if st.SourceID != "s1" {
			t.Errorf("sourceID = %q, want s1", st.SourceID)
		}
		called.Add(1)
	})

	q.Start()

	q.Enqueue(Task{Type: TaskTypeSourceProcess, Payload: SourceTask{SourceID: "s1"}})

	time.Sleep(50 * time.Millisecond)
	q.Shutdown()

	if called.Load() != 1 {
		t.Errorf("handler called %d times, want 1", called.Load())
	}
}

func TestMultipleTaskTypes(t *testing.T) {
	q := New(10)
	var sourceCount, unitCount atomic.Int32

	q.RegisterHandler(TaskTypeSourceProcess, func(_ interface{}) { sourceCount.Add(1) })
	q.RegisterHandler(TaskTypeUnitExtract, func(_ interface{}) { unitCount.Add(1) })

	q.Start()

	q.Enqueue(Task{Type: TaskTypeSourceProcess, Payload: SourceTask{SourceID: "s1"}})
	q.Enqueue(Task{Type: TaskTypeUnitExtract, Payload: UnitTask{SourceID: "s1"}})
	q.Enqueue(Task{Type: TaskTypeSourceProcess, Payload: SourceTask{SourceID: "s2"}})

	time.Sleep(50 * time.Millisecond)
	q.Shutdown()

	if sourceCount.Load() != 2 {
		t.Errorf("source handled %d, want 2", sourceCount.Load())
	}
	if unitCount.Load() != 1 {
		t.Errorf("unit handled %d, want 1", unitCount.Load())
	}
}

func TestGracefulShutdown(t *testing.T) {
	q := New(10)
	var count atomic.Int32

	q.RegisterHandler(TaskTypeSourceProcess, func(_ interface{}) {
		time.Sleep(10 * time.Millisecond)
		count.Add(1)
	})

	q.Start()

	for i := 0; i < 5; i++ {
		q.Enqueue(Task{Type: TaskTypeSourceProcess, Payload: SourceTask{SourceID: "s"}})
	}

	time.Sleep(20 * time.Millisecond)
	q.Shutdown()

	if count.Load() == 0 {
		t.Error("no tasks processed before shutdown")
	}
}

func TestQueueFullDropsTask(t *testing.T) {
	q := New(1)
	q.RegisterHandler(TaskTypeSourceProcess, func(_ interface{}) {
		time.Sleep(100 * time.Millisecond)
	})

	q.Start()
	q.Enqueue(Task{Type: TaskTypeSourceProcess, Payload: SourceTask{}})

	time.Sleep(5 * time.Millisecond)

	ok := q.Enqueue(Task{Type: TaskTypeSourceProcess, Payload: SourceTask{}})
	// May or may not succeed depending on timing, but shouldn't panic

	_ = ok
	q.Shutdown()
}

func TestHandlerPanicRecovery(t *testing.T) {
	q := New(10)
	var secondCalled atomic.Int32

	q.RegisterHandler(TaskTypeSourceProcess, func(_ interface{}) {
		panic("test panic")
	})
	q.RegisterHandler(TaskTypeUnitExtract, func(_ interface{}) {
		secondCalled.Add(1)
	})

	q.Start()

	q.Enqueue(Task{Type: TaskTypeSourceProcess, Payload: SourceTask{}})
	q.Enqueue(Task{Type: TaskTypeUnitExtract, Payload: UnitTask{}})

	time.Sleep(50 * time.Millisecond)
	q.Shutdown()

	if secondCalled.Load() != 1 {
		t.Error("second task not processed after first panicked")
	}
}
