package progress

import (
	"runtime"
	"sync"
	"testing"
)

func TestBroadcasterEmitIsSafeDuringClose(t *testing.T) {
	const sourceID = "source-1"

	for attempt := 0; attempt < 100; attempt++ {
		b := NewBroadcaster()
		b.Subscribe(sourceID)

		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < runtime.GOMAXPROCS(0); i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for j := 0; j < 100; j++ {
					b.Emit(sourceID, Event{Step: StepUnitSemantics})
				}
			}()
		}

		close(start)
		b.Close(sourceID)
		wg.Wait()
	}
}
