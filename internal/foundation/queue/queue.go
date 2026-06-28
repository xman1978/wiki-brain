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
	ch       chan Task
	handlers map[string]HandlerFunc
	wg       sync.WaitGroup
	done     chan struct{}
}

func New(bufferSize int) *Queue {
	return &Queue{
		ch:       make(chan Task, bufferSize),
		handlers: make(map[string]HandlerFunc),
		done:     make(chan struct{}),
	}
}

func (q *Queue) RegisterHandler(taskType string, handler HandlerFunc) {
	q.handlers[taskType] = handler
}

func (q *Queue) Start() {
	q.StartN(1)
}

func (q *Queue) StartN(workers int) {
	if workers < 1 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		q.wg.Add(1)
		go func() {
			defer q.wg.Done()
			for {
				select {
				case task, ok := <-q.ch:
					if !ok {
						return
					}
					q.consume(task)
				case <-q.done:
					for {
						select {
						case task, ok := <-q.ch:
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
	select {
	case q.ch <- task:
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
