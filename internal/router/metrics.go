package router

import (
	"context"
	"errors"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	meterName = "github.com/cosmo-local-credit/clc-core/internal/router"
)

type (
	metrics struct {
		loopDuration    metric.Float64Histogram
		loopCorrections metric.Int64Counter
		loopFailures    metric.Int64Counter
		enumDuration    metric.Float64Histogram
		quoteDuration   metric.Float64Histogram
		quoteOutcomes   metric.Int64Counter
		quoteMismatch   metric.Int64Counter
		simFailures     metric.Int64Counter
		unknownMember   metric.Int64Gauge
	}
)

func newMetrics(s *Service) *metrics {
	meter := otel.Meter(meterName)
	m := &metrics{}
	var errs []error
	record := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	var err error
	m.loopDuration, err = meter.Float64Histogram("router.reconcile.duration", metric.WithUnit("s"),
		metric.WithDescription("Duration of one reconciliation loop run."))
	record(err)
	m.loopCorrections, err = meter.Int64Counter("router.reconcile.corrections",
		metric.WithDescription("Values a chain read corrected; non-zero means an event was missed."))
	record(err)
	m.loopFailures, err = meter.Int64Counter("router.reconcile.failures")
	record(err)
	m.enumDuration, err = meter.Float64Histogram("router.enumeration.duration", metric.WithUnit("s"))
	record(err)
	m.quoteDuration, err = meter.Float64Histogram("router.quote.duration", metric.WithUnit("s"))
	record(err)
	m.quoteOutcomes, err = meter.Int64Counter("router.quote.outcomes",
		metric.WithDescription("Quote outcomes by reason."))
	record(err)
	m.quoteMismatch, err = meter.Int64Counter("router.quote_mismatch_total",
		metric.WithDescription("Quotes where the chain disagreed with the local arithmetic."))
	record(err)
	m.simFailures, err = meter.Int64Counter("router.simulation.failures",
		metric.WithDescription("Pre-sign simulations that reverted, by decoded reason."))
	record(err)
	m.unknownMember, err = meter.Int64Gauge("router.registry.unknown_members",
		metric.WithDescription("Registry members with no catalog voucher; they cannot be routed."))
	record(err)

	_, err = meter.Int64ObservableGauge("router.graph.version",
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			if snap := s.Snapshot(); snap != nil {
				o.Observe(int64(snap.Version))
			}
			return nil
		}))
	record(err)
	_, err = meter.Int64ObservableGauge("router.graph.age_blocks",
		metric.WithDescription("Head block minus the oldest balance block in the graph."),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			snap := s.Snapshot()
			head := s.health.headBlock()
			if snap != nil && snap.Block > 0 && head >= snap.Block {
				o.Observe(int64(head - snap.Block))
			}
			return nil
		}))
	record(err)
	_, err = meter.Int64ObservableGauge("router.dirty.depth",
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(s.dirty.depth()))
			return nil
		}))
	record(err)
	for _, e := range errs {
		otel.Handle(e) // the instruments are usable no-ops; report and carry on
	}
	return m
}

func (m *metrics) loop(ctx context.Context, name string, d time.Duration, corrections int, ok bool) {
	attrs := metric.WithAttributes(attribute.String("loop", name))
	m.loopDuration.Record(ctx, d.Seconds(), attrs)
	if corrections > 0 {
		m.loopCorrections.Add(ctx, int64(corrections), attrs)
	}
	if !ok {
		m.loopFailures.Add(ctx, 1, attrs)
	}
}

func (m *metrics) enumeration(ctx context.Context, kind string, d time.Duration) {
	m.enumDuration.Record(ctx, d.Seconds(), metric.WithAttributes(attribute.String("kind", kind)))
}

func (m *metrics) quote(ctx context.Context, d time.Duration, err error) {
	m.quoteDuration.Record(ctx, d.Seconds())
	m.quoteOutcomes.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcomeOf(err))))
}

func outcomeOf(err error) string {
	if err == nil {
		return "ok"
	}
	var (
		routeErr *RouteError
		mismatch *MismatchError
		sim      *SimulationError
		req      *RequestError
	)
	switch {
	case errors.As(err, &routeErr):
		return "route_unavailable:" + string(routeErr.Reason)
	case errors.As(err, &mismatch):
		return "quote_mismatch"
	case errors.As(err, &sim):
		return "simulation_failed"
	case errors.As(err, &req):
		return "bad_request"
	case errors.Is(err, ErrUnavailable):
		return "router_unavailable"
	}
	return "error"
}

func (m *metrics) mismatch(ctx context.Context) { m.quoteMismatch.Add(ctx, 1) }

func (m *metrics) simulationFailed(ctx context.Context, reason string) {
	m.simFailures.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}

func (m *metrics) unknownMembers(ctx context.Context, n int) { m.unknownMember.Record(ctx, int64(n)) }
