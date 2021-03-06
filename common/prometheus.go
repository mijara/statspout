package common

import (
	"net/http"
	"log"
	"flag"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/mijara/statspout/repo"
	"github.com/mijara/statspout/stats"
)

type Prometheus struct {
	cpuUsagePercent    *prometheus.GaugeVec
	memoryUsagePercent *prometheus.GaugeVec
	txBytesTotal       *prometheus.GaugeVec
	rxBytesTotal       *prometheus.GaugeVec
}

type PrometheusOpts struct {
	Address string
}

func (*Prometheus) Name() string {
	return "prometheus"
}

func (*Prometheus) Create(v interface{}) (repo.Interface, error) {
	return NewPrometheus(v.(*PrometheusOpts))
}

func (prom *Prometheus) Clear(name string) {
	prom.cpuUsagePercent.DeleteLabelValues(name)
	prom.memoryUsagePercent.DeleteLabelValues(name)
	prom.txBytesTotal.DeleteLabelValues(name)
	prom.rxBytesTotal.DeleteLabelValues(name)
}

func NewPrometheus(opts *PrometheusOpts) (*Prometheus, error) {
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

	txBytesTotal := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "tx_bytes",
			Help: "TX Bytes Total.",
		},
		[]string{"container"},
	)

	rxBytesTotal := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "rx_bytes",
			Help: "RX Bytes Total.",
		},
		[]string{"container"},
	)

	prometheus.MustRegister(cpuUsagePercent)
	prometheus.MustRegister(memoryUsagePercent)
	prometheus.MustRegister(txBytesTotal)
	prometheus.MustRegister(rxBytesTotal)

	// set handler for default Prometheus collection path.
	http.Handle("/metrics", promhttp.Handler())

	// start HTTP Server.
	go serve(opts.Address)

	return &Prometheus{
		cpuUsagePercent:    cpuUsagePercent,
		memoryUsagePercent: memoryUsagePercent,
		txBytesTotal:       txBytesTotal,
		rxBytesTotal:       rxBytesTotal,
	}, nil
}

func (prom *Prometheus) Push(s *stats.Stats) error {
	prom.cpuUsagePercent.WithLabelValues(s.Name).Set(s.CpuPercent)
	prom.memoryUsagePercent.WithLabelValues(s.Name).Set(s.MemoryPercent)
	prom.txBytesTotal.WithLabelValues(s.Name).Set(s.MemoryPercent)
	prom.rxBytesTotal.WithLabelValues(s.Name).Set(s.MemoryPercent)

	return nil
}

func (prom *Prometheus) Close() {
	// TODO
}

func serve(address string) {
	log.Fatal(http.ListenAndServe(address, nil))
}

func CreatePrometheusOpts() *PrometheusOpts {
	o := &PrometheusOpts{}

	flag.StringVar(&o.Address,
		"prometheus.address",
		":8080",
		"Address on which the Prometheus HTTP Server will publish metrics")

	return o
}
