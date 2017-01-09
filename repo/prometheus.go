package repo

import (
	"net/http"
	"log"
	"flag"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/mijara/statspout/data"
)

type Prometheus struct {
	cpuUsagePercent *prometheus.GaugeVec
	memoryUsagePercent *prometheus.GaugeVec
}

func NewPrometheus(address string) (*Prometheus, error) {
	// hacky way of removing the default Go Collector.
	prometheus.Unregister(prometheus.NewGoCollector())

	cpuUsagePercent := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "cpu_usage_percent",
			Help: "Current CPU usage percent.",
		},
		[]string{"container"},
	)

	memoryUsagePercent := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "memory_usage_percent",
			Help: "Current memory usage percent.",
		},
		[]string{"container"},
	)

	prometheus.MustRegister(cpuUsagePercent)
	prometheus.MustRegister(memoryUsagePercent)

	// set handler for default Prometheus collection path.
	http.Handle("/metrics", promhttp.Handler())

	// start HTTP Server.
	go serve(address)

	return &Prometheus{
		cpuUsagePercent: cpuUsagePercent,
		memoryUsagePercent: memoryUsagePercent,
	}, nil
}

func (prom *Prometheus) Push(stats *statspout.Stats) error {
	prom.cpuUsagePercent.WithLabelValues(stats.Name).Set(stats.CpuPercent)
	prom.memoryUsagePercent.WithLabelValues(stats.Name).Set(stats.MemoryPercent)

	return nil
}

func (prom *Prometheus) Close() {
	// TODO
}

func serve(address string) {
	log.Fatal(http.ListenAndServe(address, nil))
}

func CreatePrometheusOpts() map[string]*string {
	return map[string]*string {
		"address": flag.String(
			"prometheus.address",
			":8080",
			"Address on which the Prometheus HTTP Server will publish metrics"),
	}
}
