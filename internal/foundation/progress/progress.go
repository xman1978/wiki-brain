package progress

import (
	"sync"
)

type Event struct {
	Step      string `json:"step"`
	Status    string `json:"status"`
	Message   string `json:"message"`
	Current   int    `json:"current,omitempty"`
	Total     int    `json:"total,omitempty"`
	ElapsedMs int64  `json:"elapsed_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

const (
	StatusStarted   = "started"
	StatusCompleted = "completed"
	StatusFailed    = "failed"

	StepFormatConvert     = "format_convert"
	StepNormalize         = "normalize"
	StepOutlineStructural = "outline_structural"
	StepOutlineSemantic   = "outline_semantic"
	StepOutlineSummary    = "outline_summary"
	StepSourceSummary     = "source_summary"
	StepDomainMatch       = "domain_match"
	StepUnitSegment       = "unit_segment"
	StepUnitExtract       = "unit_extract"
	StepKPNGenerate       = "kpn_generate"
	StepConceptMatch      = "concept_match"
)

const listenerBufSize = 64

type Broadcaster struct {
	mu        sync.RWMutex
	listeners map[string][]chan Event
}

func NewBroadcaster() *Broadcaster {
	return &Broadcaster{
		listeners: make(map[string][]chan Event),
	}
}

func (b *Broadcaster) Subscribe(sourceID string) <-chan Event {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch := make(chan Event, listenerBufSize)
	b.listeners[sourceID] = append(b.listeners[sourceID], ch)
	return ch
}

func (b *Broadcaster) Unsubscribe(sourceID string, ch <-chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	chs := b.listeners[sourceID]
	for i, c := range chs {
		if c == ch {
			b.listeners[sourceID] = append(chs[:i], chs[i+1:]...)
			close(c)
			break
		}
	}
	if len(b.listeners[sourceID]) == 0 {
		delete(b.listeners, sourceID)
	}
}

func (b *Broadcaster) Emit(sourceID string, evt Event) {
	b.mu.RLock()
	chs := b.listeners[sourceID]
	b.mu.RUnlock()

	for _, ch := range chs {
		select {
		case ch <- evt:
		default:
		}
	}
}

func (b *Broadcaster) Close(sourceID string) {
	b.mu.Lock()
	chs := b.listeners[sourceID]
	delete(b.listeners, sourceID)
	b.mu.Unlock()

	for _, ch := range chs {
		close(ch)
	}
}
