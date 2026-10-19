package metrics

import (
	"maps"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// RegisterPool exports a pgx pool's statistics under pool="name" ("platform"
// or "agents"). They are read when Prometheus scrapes, so they cost nothing
// in between.
func (m *Metrics) RegisterPool(name string, pool *pgxpool.Pool) {
	if m == nil || pool == nil {
		return
	}
	m.pools.add(name, pool)
}

var (
	poolLabels   = []string{"pool"}
	descAcquired = prometheus.NewDesc("pdp_db_pool_acquired_connections", "Connections checked out of the pool now.", poolLabels, nil)
	descIdle     = prometheus.NewDesc("pdp_db_pool_idle_connections", "Idle connections in the pool.", poolLabels, nil)
	descTotal    = prometheus.NewDesc("pdp_db_pool_total_connections", "Connections the pool holds (acquired, idle and being opened).", poolLabels, nil)
	descMax      = prometheus.NewDesc("pdp_db_pool_max_connections", "The pool's size limit.", poolLabels, nil)
	descAcquires = prometheus.NewDesc("pdp_db_pool_acquires_total", "Connections checked out of the pool.", poolLabels, nil)
	descWaited   = prometheus.NewDesc("pdp_db_pool_acquires_waited_total", "Checkouts that had to wait because every connection was in use.", poolLabels, nil)
	descWaitSecs = prometheus.NewDesc("pdp_db_pool_acquire_wait_seconds_total", "Time spent waiting to check a connection out.", poolLabels, nil)
	descCanceled = prometheus.NewDesc("pdp_db_pool_canceled_acquires_total", "Checkouts abandoned (the caller's context ended) while waiting.", poolLabels, nil)
)

// poolCollector reports every registered pool (one collector for all of them:
// they share their descriptors).
type poolCollector struct {
	mu    sync.Mutex
	pools map[string]*pgxpool.Pool
}

func (c *poolCollector) add(name string, pool *pgxpool.Pool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pools == nil {
		c.pools = map[string]*pgxpool.Pool{}
	}
	c.pools[name] = pool
}

func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{descAcquired, descIdle, descTotal, descMax, descAcquires, descWaited, descWaitSecs, descCanceled} {
		ch <- d
	}
}

func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	pools := maps.Clone(c.pools)
	c.mu.Unlock()
	for name, pool := range pools {
		collectPool(ch, name, pool)
	}
}

func collectPool(ch chan<- prometheus.Metric, name string, pool *pgxpool.Pool) {
	s := pool.Stat()
	g, k := prometheus.GaugeValue, prometheus.CounterValue
	ch <- prometheus.MustNewConstMetric(descAcquired, g, float64(s.AcquiredConns()), name)
	ch <- prometheus.MustNewConstMetric(descIdle, g, float64(s.IdleConns()), name)
	ch <- prometheus.MustNewConstMetric(descTotal, g, float64(s.TotalConns()), name)
	ch <- prometheus.MustNewConstMetric(descMax, g, float64(s.MaxConns()), name)
	ch <- prometheus.MustNewConstMetric(descAcquires, k, float64(s.AcquireCount()), name)
	ch <- prometheus.MustNewConstMetric(descWaited, k, float64(s.EmptyAcquireCount()), name)
	ch <- prometheus.MustNewConstMetric(descWaitSecs, k, s.AcquireDuration().Seconds(), name)
	ch <- prometheus.MustNewConstMetric(descCanceled, k, float64(s.CanceledAcquireCount()), name)
}
