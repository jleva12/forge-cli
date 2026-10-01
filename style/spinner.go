package style

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// Spinner shows an animated message on w until the returned stop function is
// called. It does nothing unless w is a color-capable terminal, and it only
// appears if the work takes longer than a short delay, so fast operations
// don't flicker.
func Spinner(w io.Writer, msg string) (stop func()) {
	p := For(w)
	if !p.on {
		return func() {}
	}
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-done:
			return
		case <-time.After(250 * time.Millisecond):
		}
		tick := time.NewTicker(80 * time.Millisecond)
		defer tick.Stop()
		for i := 0; ; i++ {
			fmt.Fprintf(w, "\r%s %s", p.Cyan(frames[i%len(frames)]), p.Dim(msg))
			select {
			case <-done:
				fmt.Fprint(w, "\r\x1b[2K")
				return
			case <-tick.C:
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			wg.Wait()
		})
	}
}
