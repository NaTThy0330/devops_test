package prometheus

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

var DefBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type CounterOpts struct {
	Name string
	Help string
}

type HistogramOpts struct {
	Name    string
	Help    string
	Buckets []float64
}

type Collector interface {
	render() []string
}

type Registry struct {
	mu         sync.Mutex
	collectors []Collector
}

func NewRegistry() *Registry {
	return &Registry{}
}

func (r *Registry) MustRegister(cs ...Collector) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.collectors = append(r.collectors, cs...)
}

func (r *Registry) Render() string {
	r.mu.Lock()
	collectors := append([]Collector(nil), r.collectors...)
	r.mu.Unlock()

	var lines []string
	for _, collector := range collectors {
		lines = append(lines, collector.render()...)
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

type counterSample struct {
	labels []string
	value  float64
}

type CounterVec struct {
	opts       CounterOpts
	labelNames []string
	mu         sync.Mutex
	values     map[string]*counterSample
}

type Counter struct {
	vec  *CounterVec
	key  string
	samp *counterSample
}

func NewCounterVec(opts CounterOpts, labelNames []string) *CounterVec {
	return &CounterVec{
		opts:       opts,
		labelNames: append([]string(nil), labelNames...),
		values:     map[string]*counterSample{},
	}
}

func (c *CounterVec) WithLabelValues(vals ...string) Counter {
	if len(vals) != len(c.labelNames) {
		panic(fmt.Sprintf("expected %d label values, got %d", len(c.labelNames), len(vals)))
	}
	key := strings.Join(vals, "\x1f")

	c.mu.Lock()
	defer c.mu.Unlock()

	sample, ok := c.values[key]
	if !ok {
		sample = &counterSample{labels: append([]string(nil), vals...)}
		c.values[key] = sample
	}

	return Counter{vec: c, key: key, samp: sample}
}

func (c Counter) Inc() {
	c.vec.mu.Lock()
	defer c.vec.mu.Unlock()
	c.samp.value++
}

func (c *CounterVec) render() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	lines := []string{
		fmt.Sprintf("# HELP %s %s", c.opts.Name, c.opts.Help),
		fmt.Sprintf("# TYPE %s counter", c.opts.Name),
	}

	keys := make([]string, 0, len(c.values))
	for key := range c.values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		sample := c.values[key]
		lines = append(lines, fmt.Sprintf("%s%s %g", c.opts.Name, formatLabels(c.labelNames, sample.labels), sample.value))
	}

	return lines
}

type histogramSample struct {
	labels []string
	counts []uint64
	sum    float64
	count  uint64
}

type HistogramVec struct {
	opts       HistogramOpts
	labelNames []string
	buckets    []float64
	mu         sync.Mutex
	values     map[string]*histogramSample
}

type Histogram struct {
	vec  *HistogramVec
	samp *histogramSample
}

func NewHistogramVec(opts HistogramOpts, labelNames []string) *HistogramVec {
	buckets := opts.Buckets
	if len(buckets) == 0 {
		buckets = DefBuckets
	}
	copied := append([]float64(nil), buckets...)
	return &HistogramVec{
		opts:       opts,
		labelNames: append([]string(nil), labelNames...),
		buckets:    copied,
		values:     map[string]*histogramSample{},
	}
}

func (h *HistogramVec) WithLabelValues(vals ...string) Histogram {
	if len(vals) != len(h.labelNames) {
		panic(fmt.Sprintf("expected %d label values, got %d", len(h.labelNames), len(vals)))
	}
	key := strings.Join(vals, "\x1f")

	h.mu.Lock()
	defer h.mu.Unlock()

	sample, ok := h.values[key]
	if !ok {
		sample = &histogramSample{
			labels: append([]string(nil), vals...),
			counts: make([]uint64, len(h.buckets)+1),
		}
		h.values[key] = sample
	}

	return Histogram{vec: h, samp: sample}
}

func (h Histogram) Observe(v float64) {
	h.vec.mu.Lock()
	defer h.vec.mu.Unlock()

	h.samp.count++
	h.samp.sum += v
	for i, bucket := range h.vec.buckets {
		if v <= bucket {
			h.samp.counts[i]++
		}
	}
	h.samp.counts[len(h.vec.buckets)]++
}

func (h *HistogramVec) render() []string {
	h.mu.Lock()
	defer h.mu.Unlock()

	lines := []string{
		fmt.Sprintf("# HELP %s %s", h.opts.Name, h.opts.Help),
		fmt.Sprintf("# TYPE %s histogram", h.opts.Name),
	}

	keys := make([]string, 0, len(h.values))
	for key := range h.values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		sample := h.values[key]
		for i, bucket := range h.buckets {
			labels := append([]string(nil), sample.labels...)
			labels = append(labels, fmt.Sprintf("le=\"%g\"", bucket))
			lines = append(lines, fmt.Sprintf("%s_bucket%s %d", h.opts.Name, formatInlineLabels(labels), sample.counts[i]))
		}
		labels := append([]string(nil), sample.labels...)
		labels = append(labels, `le="+Inf"`)
		lines = append(lines, fmt.Sprintf("%s_bucket%s %d", h.opts.Name, formatInlineLabels(labels), sample.counts[len(h.buckets)]))
		lines = append(lines, fmt.Sprintf("%s_sum%s %g", h.opts.Name, formatLabels(h.labelNames, sample.labels), sample.sum))
		lines = append(lines, fmt.Sprintf("%s_count%s %d", h.opts.Name, formatLabels(h.labelNames, sample.labels), sample.count))
	}

	return lines
}

func formatLabels(labelNames, values []string) string {
	if len(values) == 0 {
		return ""
	}
	pairs := make([]string, 0, len(values))
	for i := range values {
		pairs = append(pairs, fmt.Sprintf(`%s="%s"`, labelNames[i], escapeLabel(values[i])))
	}
	return "{" + strings.Join(pairs, ",") + "}"
}

func formatInlineLabels(labels []string) string {
	if len(labels) == 0 {
		return ""
	}
	return "{" + strings.Join(labels, ",") + "}"
}

func escapeLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return value
}
