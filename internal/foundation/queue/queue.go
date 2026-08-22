package queue

import (
	"log/slog"
	"sync"
)

const (
	TaskTypeSourceProcess = "source_process"
	TaskTypeUnitExtract   = "unit_extract"
	TaskTypeTrace         = "trace_write"
)

type Task struct {
	Type    string
	Payload interface{}
}

type SourceTask struct {
	SourceID string
}

type UnitTask struct {
	SourceID string
}

type TraceTask struct {
	Result interface{}
}

type HandlerFunc func(payload interface{})

type Queue struct {
	ch        chan Task
	handlers  map[string]HandlerFunc
	dedicated map[string]chan Task
	bufSize   int
	wg        sync.WaitGroup
	done      chan struct{}
}

func New(bufferSize int) *Queue {
	return &Queue{
		ch:        make(chan Task, bufferSize),
		handlers:  make(map[string]HandlerFunc),
		dedicated: make(map[string]chan Task),
		bufSize:   bufferSize,
		done:      make(chan struct{}),
	}
}

func (q *Queue) RegisterHandler(taskType string, handler HandlerFunc) {
	q.handlers[taskType] = handler
}

// RegisterHandlerWithWorkers registers handler for taskType on its own
// dedicated channel and worker pool, isolated from the shared pool started
// by Start/StartN and from every other task type — tasks of this type never
// wait behind, or block, tasks of another type, and vice versa. Must be
// called before any Enqueue of this task type. Use this when a task type
// needs its own concurrency cap distinct from queue.workers.
func (q *Queue) RegisterHandlerWithWorkers(taskType string, workers int, handler HandlerFunc) {
	if workers < 1 {
		workers = 1
	}
	q.handlers[taskType] = handler
	ch := make(chan Task, q.bufSize)
	q.dedicated[taskType] = ch
	q.startWorkers(ch, workers)
}

func (q *Queue) Start() {
	q.StartN(1)
}

func (q *Queue) StartN(workers int) {
	if workers < 1 {
		workers = 1
	}
	q.startWorkers(q.ch, workers)
}

func (q *Queue) startWorkers(ch chan Task, workers int) {
	for i := 0; i < workers; i++ {
		q.wg.Add(1)
		go func() {
			defer q.wg.Done()
			for {
				select {
				case task, ok := <-ch:
					if !ok {
						return
					}
					q.consume(task)
				case <-q.done:
					for {
						select {
						case task, ok := <-ch:
							if !ok {
								return
							}
							q.consume(task)
						default:
							return
						}
					}
				}
			}
		}()
	}
}

func (q *Queue) Enqueue(task Task) bool {
	ch := q.ch
	if dedicated, ok := q.dedicated[task.Type]; ok {
		ch = dedicated
	}
	select {
	case ch <- task:
		return true
	default:
		slog.Error("queue full, task dropped", "type", task.Type)
		return false
	}
}

func (q *Queue) Shutdown() {
	close(q.done)
	q.wg.Wait()
}

func (q *Queue) consume(task Task) {
	handler, ok := q.handlers[task.Type]
	if !ok {
		slog.Error("unknown task type", "type", task.Type)
		return
	}

	defer func() {
		if r := recover(); r != nil {
			slog.Error("task handler panicked", "type", task.Type, "panic", r)
		}
	}()

	handler(task.Payload)
}
