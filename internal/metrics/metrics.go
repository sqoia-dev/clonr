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
)
