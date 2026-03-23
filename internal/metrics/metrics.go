package metrics

import (
	"sync"
	"time"
)

type Registry struct {
	mu sync.Mutex

	requestsTotal map[string]int64
	errorsTotal   map[string]int64

	packetBudgetRejectionsTotal int64
	compactionStageCounts       map[string]int64

	latencies  map[string]*timingStat
	backendOps map[string]*timingStat

	envelopeBytes bytesStat
	bodyBytes     bytesStat

	backendMode string
	modelMode   string
}

type timingStat struct {
	Count   int64   `json:"count"`
	TotalMS float64 `json:"total_ms"`
	AvgMS   float64 `json:"avg_ms"`
	MaxMS   float64 `json:"max_ms"`
}

type bytesStat struct {
	Count int64   `json:"count"`
	Total int64   `json:"total"`
	Avg   float64 `json:"avg"`
	Max   int     `json:"max"`
}

type Snapshot struct {
	RequestsTotal               map[string]int64      `json:"requests_total"`
	ErrorsTotal                 map[string]int64      `json:"errors_total"`
	PacketBudgetRejectionsTotal int64                 `json:"packet_budget_rejections_total"`
	CompactionStageCounts       map[string]int64      `json:"compaction_stage_counts"`
	LatenciesMS                 map[string]timingStat `json:"latencies_ms"`
	BackendOpsMS                map[string]timingStat `json:"backend_ops_ms"`
	EnvelopeBytes               bytesStat             `json:"envelope_bytes"`
	BodyBytes                   bytesStat             `json:"body_bytes"`
	BackendMode                 string                `json:"backend_mode"`
	ModelMode                   string                `json:"model_mode"`
}

func NewRegistry() *Registry {
	return &Registry{
		requestsTotal:         make(map[string]int64),
		errorsTotal:           make(map[string]int64),
		compactionStageCounts: make(map[string]int64),
		latencies:             make(map[string]*timingStat),
		backendOps:            make(map[string]*timingStat),
	}
}

func (r *Registry) SetModes(backendMode, modelMode string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.backendMode = backendMode
	r.modelMode = modelMode
}

func (r *Registry) IncRequest(route string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requestsTotal[route]++
}

func (r *Registry) IncError(route string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errorsTotal[route]++
}

func (r *Registry) IncPacketBudgetRejection() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.packetBudgetRejectionsTotal++
}

func (r *Registry) IncCompactionStage(stage int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := "stage_" + itoa(stage)
	r.compactionStageCounts[key]++
}

func (r *Registry) ObserveLatency(name string, d time.Duration) {
	if r == nil {
		return
	}
	ms := float64(d.Microseconds()) / 1000.0
	r.mu.Lock()
	defer r.mu.Unlock()
	stat := r.latencies[name]
	if stat == nil {
		stat = &timingStat{}
		r.latencies[name] = stat
	}
	stat.Count++
	stat.TotalMS += ms
	if ms > stat.MaxMS {
		stat.MaxMS = ms
	}
	stat.AvgMS = stat.TotalMS / float64(stat.Count)
}

func (r *Registry) ObserveBackendOp(name string, d time.Duration) {
	if r == nil {
		return
	}
	ms := float64(d.Microseconds()) / 1000.0
	r.mu.Lock()
	defer r.mu.Unlock()
	stat := r.backendOps[name]
	if stat == nil {
		stat = &timingStat{}
		r.backendOps[name] = stat
	}
	stat.Count++
	stat.TotalMS += ms
	if ms > stat.MaxMS {
		stat.MaxMS = ms
	}
	stat.AvgMS = stat.TotalMS / float64(stat.Count)
}

func (r *Registry) ObserveEnvelopeBytes(n int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.envelopeBytes.Count++
	r.envelopeBytes.Total += int64(n)
	if n > r.envelopeBytes.Max {
		r.envelopeBytes.Max = n
	}
	r.envelopeBytes.Avg = float64(r.envelopeBytes.Total) / float64(r.envelopeBytes.Count)
}

func (r *Registry) ObserveBodyBytes(n int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bodyBytes.Count++
	r.bodyBytes.Total += int64(n)
	if n > r.bodyBytes.Max {
		r.bodyBytes.Max = n
	}
	r.bodyBytes.Avg = float64(r.bodyBytes.Total) / float64(r.bodyBytes.Count)
}

func (r *Registry) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	latencies := make(map[string]timingStat, len(r.latencies))
	for k, v := range r.latencies {
		latencies[k] = *v
	}
	backendOps := make(map[string]timingStat, len(r.backendOps))
	for k, v := range r.backendOps {
		backendOps[k] = *v
	}
	requests := make(map[string]int64, len(r.requestsTotal))
	for k, v := range r.requestsTotal {
		requests[k] = v
	}
	errors := make(map[string]int64, len(r.errorsTotal))
	for k, v := range r.errorsTotal {
		errors[k] = v
	}
	compaction := make(map[string]int64, len(r.compactionStageCounts))
	for k, v := range r.compactionStageCounts {
		compaction[k] = v
	}

	return Snapshot{
		RequestsTotal:               requests,
		ErrorsTotal:                 errors,
		PacketBudgetRejectionsTotal: r.packetBudgetRejectionsTotal,
		CompactionStageCounts:       compaction,
		LatenciesMS:                 latencies,
		BackendOpsMS:                backendOps,
		EnvelopeBytes:               r.envelopeBytes,
		BodyBytes:                   r.bodyBytes,
		BackendMode:                 r.backendMode,
		ModelMode:                   r.modelMode,
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	buf := [20]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + (n % 10))
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
