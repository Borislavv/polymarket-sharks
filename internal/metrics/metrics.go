// Package metrics is a deliberately tiny in-process counter/gauge store
// exposed at /metrics in a Prometheus-ish text format. No external dep
// to keep the binary small. Counters monotonically increase; gauges
// represent the latest sampled value.
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
)

type Registry struct {
	mu       sync.RWMutex
	counters map[string]*int64
	gauges   map[string]*int64
}

var Default = &Registry{
	counters: map[string]*int64{},
	gauges:   map[string]*int64{},
}

func (r *Registry) Counter(name string) *int64 {
	r.mu.RLock()
	c, ok := r.counters[name]
	r.mu.RUnlock()
	if ok {
		return c
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.counters[name]; ok {
		return c
	}
	v := new(int64)
	r.counters[name] = v
	return v
}

func (r *Registry) gauge(name string) *int64 {
	r.mu.RLock()
	g, ok := r.gauges[name]
	r.mu.RUnlock()
	if ok {
		return g
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if g, ok := r.gauges[name]; ok {
		return g
	}
	v := new(int64)
	r.gauges[name] = v
	return v
}

func Inc(name string)          { atomic.AddInt64(Default.Counter(name), 1) }
func Add(name string, n int64) { atomic.AddInt64(Default.Counter(name), n) }

// SetGauge stores the latest value for a gauge metric. Subsequent sets
// overwrite the previous value (unlike Add which accumulates).
func SetGauge(name string, v int64) { atomic.StoreInt64(Default.gauge(name), v) }

// GaugeValue returns the current gauge value (for tests).
func GaugeValue(name string) int64 { return atomic.LoadInt64(Default.gauge(name)) }

// CounterValue returns the current counter value (read-only snapshot).
func CounterValue(name string) int64 { return atomic.LoadInt64(Default.Counter(name)) }

// Handler renders both counters and gauges as `wt_<name> <value>` lines.
// Counters and gauges share the same prefix in the text output but use
// disjoint name pools internally.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		r.mu.RLock()
		cnames := make([]string, 0, len(r.counters))
		for k := range r.counters {
			cnames = append(cnames, k)
		}
		gnames := make([]string, 0, len(r.gauges))
		for k := range r.gauges {
			gnames = append(gnames, k)
		}
		r.mu.RUnlock()
		sort.Strings(cnames)
		sort.Strings(gnames)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		for _, n := range cnames {
			fmt.Fprintf(w, "wt_%s %d\n", n, atomic.LoadInt64(r.counters[n]))
		}
		for _, n := range gnames {
			fmt.Fprintf(w, "wt_%s %d\n", n, atomic.LoadInt64(r.gauges[n]))
		}
	})
}
