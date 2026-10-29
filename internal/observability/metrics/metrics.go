// Package metrics owns the Prometheus instruments exposed on GET /metrics.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/fx"
)

var Module = fx.Provide(New)

type Registry struct {
	registry *prometheus.Registry

	sandboxesActive   *prometheus.GaugeVec
	sandboxLifecycle  *prometheus.CounterVec
	sandboxCreateTime prometheus.Histogram
	execTotal         *prometheus.CounterVec
	execDuration      *prometheus.HistogramVec
	terminalsActive   prometheus.Gauge

	httpDuration *prometheus.HistogramVec
	httpRequests *prometheus.CounterVec
}

func New() *Registry {
	registry := prometheus.NewRegistry()

	r := &Registry{
		registry: registry,
		sandboxesActive: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "sandboxd_sandboxes_active",
			Help: "Sandboxes currently held by this process, by network mode.",
		}, []string{"network"}),
		sandboxLifecycle: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sandboxd_sandbox_events_total",
			Help: "Sandbox lifecycle events: created, create_failed, destroyed, expired, idle, failed.",
		}, []string{"event"}),
		sandboxCreateTime: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "sandboxd_sandbox_create_duration_seconds",
			Help:    "Time to bring a sandbox from request to running.",
			Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		}),
		execTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sandboxd_exec_total",
			Help: "Commands executed, by kind (exec, stream, session) and outcome (ok, nonzero, timeout, error).",
		}, []string{"kind", "outcome"}),
		execDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "sandboxd_exec_duration_seconds",
			Help:    "Command wall-clock duration by kind.",
			Buckets: []float64{.005, .01, .05, .1, .5, 1, 5, 10, 30, 60, 300, 600},
		}, []string{"kind"}),
		terminalsActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "sandboxd_terminals_active",
			Help: "Interactive terminals currently attached.",
		}),

		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_server_request_duration_seconds",
			Help:    "HTTP request duration by method, route template, and status.",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		}, []string{"method", "route", "status"}),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_server_requests_total",
			Help: "HTTP requests by method, route template, and status.",
		}, []string{"method", "route", "status"}),
	}

	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		r.sandboxesActive, r.sandboxLifecycle, r.sandboxCreateTime,
		r.execTotal, r.execDuration, r.terminalsActive,
		r.httpDuration, r.httpRequests,
	)

	return r
}

func (r *Registry) Handler() http.Handler {
	if r == nil {
		return http.NotFoundHandler()
	}

	return promhttp.HandlerFor(r.registry, promhttp.HandlerOpts{})
}

func (r *Registry) Gatherer() prometheus.Gatherer {
	if r == nil {
		return prometheus.NewRegistry()
	}

	return r.registry
}

func (r *Registry) SandboxStarted(network string) {
	if r == nil {
		return
	}
	r.sandboxesActive.WithLabelValues(network).Inc()
}

func (r *Registry) SandboxStopped(network string) {
	if r == nil {
		return
	}
	r.sandboxesActive.WithLabelValues(network).Dec()
}

func (r *Registry) SandboxEvent(event string) {
	if r == nil {
		return
	}
	r.sandboxLifecycle.WithLabelValues(event).Inc()
}

func (r *Registry) ObserveSandboxCreate(duration time.Duration) {
	if r == nil {
		return
	}
	r.sandboxCreateTime.Observe(duration.Seconds())
}

func (r *Registry) ObserveExec(kind, outcome string, duration time.Duration) {
	if r == nil {
		return
	}
	r.execTotal.WithLabelValues(kind, outcome).Inc()
	r.execDuration.WithLabelValues(kind).Observe(duration.Seconds())
}

func (r *Registry) TerminalOpened() {
	if r == nil {
		return
	}
	r.terminalsActive.Inc()
}

func (r *Registry) TerminalClosed() {
	if r == nil {
		return
	}
	r.terminalsActive.Dec()
}

func (r *Registry) ObserveHTTP(method, route string, status int, duration time.Duration) {
	if r == nil {
		return
	}

	statusLabel := strconv.Itoa(status)
	r.httpRequests.WithLabelValues(method, route, statusLabel).Inc()
	r.httpDuration.WithLabelValues(method, route, statusLabel).Observe(duration.Seconds())
}
