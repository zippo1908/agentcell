  package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Platform metrics, registered once with controller-runtime's own registry
// (the same one served on --metrics-addr) so nothing extra needs wiring in
// main.go.
var (
	// cellActiveSessions reports the same count as Cell.Status.ActiveSessions,
	// broken out per cell so a dashboard can chart load across the fleet
	// without listing Cells itself.
	cellActiveSessions = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "agentcell_cell_active_sessions",
			Help: "Number of active (queued, running, or settling) sessions in a cell.",
		},
		[]string{"cell"},
	)

	// cellsTotal is the count of Cell resources currently known to this
	// reconciler's control namespace.
	cellsTotal = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "agentcell_cells_total",
			Help: "Total number of Cell resources currently reconciled.",
		},
	)
)

func init() {
	metrics.Registry.MustRegister(cellActiveSessions, cellsTotal)
}