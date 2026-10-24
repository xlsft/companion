// Command restraint prints the notification restraint state every 2 s.
// Spike for OC-72; not shipped.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"

	"github.com/xlsft/openclick/internal/restraint"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	m := restraint.NewMonitor(platformSignals(), restraint.WithInterval(2*time.Second))
	go m.Run(ctx)
	p := restraint.NewPolicy(m)

	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			st := p.State()
			diag := m.Diagnostics()
			var keys []string
			for k := range diag {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			var parts []string
			for _, k := range keys {
				parts = append(parts, k+"="+diag[k])
			}
			fmt.Printf("%s restrained=%v reasons=%v spontaneous=%v diag=[%s]\n",
				time.Now().Format("15:04:05"), st.Restrained, st.Reasons, p.Allow(restraint.Spontaneous), strings.Join(parts, "; "))
		}
	}
}
