package telemetry

import (
	"context"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// ObservePool reports connection pool usage for pool. It is a no-op until
// Setup installs a real MeterProvider.
func ObservePool(name string, pool *pgxpool.Pool) error {
	meter := otel.Meter(MeterName)

	count, err := meter.Int64ObservableUpDownCounter("db.client.connection.count",
		metric.WithUnit("{connection}"),
		metric.WithDescription("Connections in the pool, by state."),
	)
	if err != nil {
		return err
	}
	limit, err := meter.Int64ObservableUpDownCounter("db.client.connection.max",
		metric.WithUnit("{connection}"),
		metric.WithDescription("Maximum number of connections allowed in the pool."),
	)
	if err != nil {
		return err
	}
	// Acquires that found no idle connection are the earliest sign of an
	// undersized pool.
	empty, err := meter.Int64ObservableCounter("clc.db.client.connection.empty_acquires",
		metric.WithUnit("{acquire}"),
		metric.WithDescription("Acquires that had to wait because no idle connection was available."),
	)
	if err != nil {
		return err
	}

	poolName := attribute.String("db.client.connection.pool.name", name)
	idle := metric.WithAttributes(poolName, attribute.String("db.client.connection.state", "idle"))
	used := metric.WithAttributes(poolName, attribute.String("db.client.connection.state", "used"))
	pooled := metric.WithAttributes(poolName)

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		s := pool.Stat()
		o.ObserveInt64(count, int64(s.IdleConns()), idle)
		o.ObserveInt64(count, int64(s.AcquiredConns()), used)
		o.ObserveInt64(limit, int64(s.MaxConns()), pooled)
		o.ObserveInt64(empty, s.EmptyAcquireCount(), pooled)
		return nil
	}, count, limit, empty)
	return err
}

type (
	// HTTPServerDuration records inbound request latency.
	HTTPServerDuration struct {
		hist metric.Float64Histogram
	}
)

func NewHTTPServerDuration() *HTTPServerDuration {
	hist, err := otel.Meter(MeterName).Float64Histogram("http.server.request.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of inbound HTTP requests."),
		metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10),
	)
	if err != nil {
		// The returned instrument is a usable no-op; report and carry on.
		otel.Handle(err)
	}
	return &HTTPServerDuration{hist: hist}
}

// Record takes the matched route template (e.g. /users/:id), never the raw
// path, so scanners hitting random URLs cannot blow up cardinality.
func (d *HTTPServerDuration) Record(method, route string, status int, latency time.Duration) {
	if route == "" {
		route = "unmatched"
	}
	d.hist.Record(context.Background(), latency.Seconds(), metric.WithAttributes(
		attribute.String("http.request.method", method),
		attribute.String("http.route", route),
		attribute.String("http.response.status_code", strconv.Itoa(status)),
	))
}
