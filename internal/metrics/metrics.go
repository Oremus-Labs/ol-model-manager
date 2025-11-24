package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	jobDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "model_manager_job_duration_seconds",
		Help:    "Duration of asynchronous jobs executed by the worker",
		Buckets: []float64{1, 5, 15, 30, 60, 120, 300, 600, 1800, 3600, 7200},
	}, []string{"type", "status"})

	jobStatusTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "model_manager_job_status_total",
		Help: "Total jobs completed grouped by type and status",
	}, []string{"type", "status"})

	hfRefreshDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "model_manager_hf_refresh_duration_seconds",
		Help:    "Duration of Hugging Face metadata refresh cycles",
		Buckets: []float64{5, 15, 30, 60, 120, 300},
	})

	hfRefreshCount = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "model_manager_hf_refresh_total",
		Help: "Number of Hugging Face refresh cycles grouped by outcome",
	}, []string{"status"})

	hfRefreshModels = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "model_manager_hf_models_cached",
		Help: "Number of Hugging Face models cached after the most recent refresh",
	})
)

// ObserveJobCompletion records the duration and status of a completed job.
func ObserveJobCompletion(jobType, status string, duration time.Duration) {
	if jobType == "" {
		jobType = "unknown"
	}
	if status == "" {
		status = "unknown"
	}
	jobDuration.WithLabelValues(jobType, status).Observe(duration.Seconds())
	jobStatusTotal.WithLabelValues(jobType, status).Inc()
}

// ObserveHFRefresh records metrics for Hugging Face sync cycles.
func ObserveHFRefresh(duration time.Duration, count int, success bool) {
	hfRefreshDuration.Observe(duration.Seconds())
	if success {
		hfRefreshCount.WithLabelValues("success").Inc()
		hfRefreshModels.Set(float64(count))
	} else {
		hfRefreshCount.WithLabelValues("failed").Inc()
	}
}
