// Package telemetry ships logs and a small set of metrics to an OpenTelemetry
// Collector over OTLP/HTTP.
//
// It is inert unless OTEL_EXPORTER_OTLP_ENDPOINT is set, so local runs and
// tests need no collector. Everything else (headers, TLS, resource attributes,
// service name) follows the standard OTEL_* environment variables.
package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
)

const (
	envEndpoint       = "OTEL_EXPORTER_OTLP_ENDPOINT"
	envLogsEndpoint   = "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"
	envMetricInterval = "OTEL_METRIC_EXPORT_INTERVAL"

	// Logs are the primary signal and flush often; metrics are a bonus and
	// only need a coarse resolution.
	logFlushInterval      = 5 * time.Second
	defaultMetricInterval = 5 * time.Minute

	// MeterName is the instrumentation scope for application metrics.
	MeterName = "github.com/cosmo-local-credit/clc-core"
)

// Enabled reports whether an OTLP endpoint is configured.
func Enabled() bool {
	return os.Getenv(envEndpoint) != "" || os.Getenv(envLogsEndpoint) != ""
}

// Setup wires OTLP log and metric export. It returns a handler that writes to
// base and to the collector, and a shutdown func that flushes both signals.
// The collector receives exactly the records base would emit, so DEBUG/DEV
// level switches apply to both. When no endpoint is configured it returns base
// and a no-op shutdown.
func Setup(ctx context.Context, base slog.Handler, o Opts) (slog.Handler, func(context.Context) error, error) {
	if !Enabled() {
		return base, func(context.Context) error { return nil }, nil
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(o.ServiceName),
			semconv.ServiceVersion(o.Version),
		),
		// Applied after the defaults above, so OTEL_SERVICE_NAME and
		// OTEL_RESOURCE_ATTRIBUTES (e.g. deployment.environment.name) win.
		resource.WithFromEnv(),
		resource.WithHost(),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		return nil, nil, err
	}

	logExp, err := otlploghttp.New(ctx)
	if err != nil {
		return nil, nil, err
	}
	logProvider := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp,
			sdklog.WithExportInterval(logFlushInterval),
		)),
	)

	metricExp, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return nil, nil, errors.Join(err, logProvider.Shutdown(ctx))
	}
	readerOpts := []sdkmetric.PeriodicReaderOption{}
	if os.Getenv(envMetricInterval) == "" {
		readerOpts = append(readerOpts, sdkmetric.WithInterval(defaultMetricInterval))
	}
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp, readerOpts...)),
	)
	otel.SetMeterProvider(meterProvider)

	if err := runtime.Start(runtime.WithMeterProvider(meterProvider)); err != nil {
		return nil, nil, errors.Join(err, logProvider.Shutdown(ctx), meterProvider.Shutdown(ctx))
	}

	otelHandler := &gatedHandler{
		Handler: otelslog.NewHandler(o.ServiceName, otelslog.WithLoggerProvider(logProvider)),
		gate:    base,
	}

	shutdown := func(ctx context.Context) error {
		// Logs first: a failing metric flush should not drop the final lines.
		return errors.Join(logProvider.Shutdown(ctx), meterProvider.Shutdown(ctx))
	}

	return slog.NewMultiHandler(base, otelHandler), shutdown, nil
}

type (
	Opts struct {
		ServiceName string
		Version     string
	}

	// gatedHandler defers the level decision to gate. otelslog would otherwise
	// forward every record it is offered, including DEBUG lines that stderr
	// drops.
	gatedHandler struct {
		slog.Handler

		gate slog.Handler
	}
)

func (h *gatedHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.gate.Enabled(ctx, l)
}

func (h *gatedHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &gatedHandler{Handler: h.Handler.WithAttrs(attrs), gate: h.gate}
}

func (h *gatedHandler) WithGroup(name string) slog.Handler {
	return &gatedHandler{Handler: h.Handler.WithGroup(name), gate: h.gate}
}
