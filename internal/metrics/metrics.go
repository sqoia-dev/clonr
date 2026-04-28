// Package metrics provides Prometheus instrumentation for clustr-serverd.
//
// Registered metrics:
//
//   clustr_active_deploys              gauge    — nodes currently in reimage/deploy state
//   clustr_deploy_total{status}        counter  — cumulative completed reimages by terminal status
//   clustr_api_requests_total{endpoint,status,method}  counter — HTTP requests
//   clustr_db_size_bytes               gauge    — SQLite database file size
//   clustr_image_disk_bytes            gauge    — total bytes used by image blobs
//   clustr_node_count{state}           gauge    — node count bucketed by lifecycle state
//   clustr_flipback_failures_total     counter  — verify-boot flip-back failures (S4-9)
//   clustr_webhook_deliveries_total{event,status}  counter — outbound webhook deliveries
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// ActiveDeploys is the number of nodes currently in a non-terminal reimage state.
	ActiveDeploys = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "clustr_active_deploys",
		Help: "Number of nodes currently in a non-terminal reimage/deploy state.",
	})

	// DeployTotal counts completed reimages by terminal status (complete, failed, canceled).
	DeployTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "clustr_deploy_total",
		Help: "Total number of completed reimage requests by terminal status.",
	}, []string{"status"})

	// APIRequestsTotal counts HTTP requests by endpoint group, HTTP status code, and method.
	APIRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "clustr_api_requests_total",
		Help: "Total number of HTTP API requests by endpoint, status code, and method.",
	}, []string{"endpoint", "status", "method"})

	// DBSizeBytes is the SQLite database file size in bytes.
	DBSizeBytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "clustr_db_size_bytes",
		Help: "SQLite database file size in bytes.",
	})

	// ImageDiskBytes is the total bytes used by image blobs in CLUSTR_IMAGE_DIR.
	ImageDiskBytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "clustr_image_disk_bytes",
		Help: "Total bytes used by image blobs in the image directory.",
	})

	// NodeCount is the number of nodes bucketed by lifecycle state.
	NodeCount = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "clustr_node_count",
		Help: "Number of nodes in each lifecycle state.",
	}, []string{"state"})

	// FlipBackFailures counts verify-boot flip-back failures (S4-9).
	FlipBackFailures = promauto.NewCounter(prometheus.CounterOpts{
		Name: "clustr_flipback_failures_total",
		Help: "Total number of verify-boot flipNodeToDiskFirst failures after deploy_verified_booted.",
	})

	// WebhookDeliveries counts outbound webhook delivery attempts by event and status (S4-2).
	WebhookDeliveries = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "clustr_webhook_deliveries_total",
		Help: "Total outbound webhook delivery attempts by event type and delivery status.",
	}, []string{"event", "status"})

	// TechTrigFired is a gauge (0 or 1) per TECH-TRIG signal (Sprint M, v1.11.0).
	// Labels: name = trigger name (t1_postgresql, t2_framework, t3_multitenant, t4_log_archive).
	// Value: 1 = trigger has fired, 0 = not fired.
	TechTrigFired = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "clustr_tech_trigger",
		Help: "Whether each D27 TECH-TRIG signal has fired (1 = fired, 0 = not fired).",
	}, []string{"name"})

	// TechTrigValue is the most recent primary metric value for each TECH-TRIG signal.
	// Labels: name = trigger name.
	// Value semantics per trigger:
	//   t1_postgresql  — node count (metric A; contention rate tracked in logs only)
	//   t2_framework   — frontend JS LOC
	//   t3_multitenant — 0 (no numeric primary metric; binary manual signal)
	//   t4_log_archive — estimated log storage bytes
	TechTrigValue = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "clustr_tech_trigger_value",
		Help: "Current primary metric value for each D27 TECH-TRIG signal.",
	}, []string{"name"})
)
