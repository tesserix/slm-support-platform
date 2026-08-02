// Package observability wires OpenTelemetry traces and metrics for
// slm-router. Exporters speak OTLP over gRPC to the in-cluster collector.
//
// Init is a no-op when OTEL_EXPORTER_OTLP_ENDPOINT is empty, so local
// runs and tests don't try to dial a collector that isn't there.
package observability

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	// MUST match the schema resource.Default() carries for this SDK version —
	// a mismatch makes resource.Merge error and (pre-fix) crash-looped the
	// service. SDK v1.44 defaults to schema 1.41.0.
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
)

// defaultEndpoint is used when OTEL_EXPORTER_OTLP_ENDPOINT is unset but a
// caller still wants telemetry. In practice the deployment sets the env
// explicitly; this mirrors the in-cluster collector address.
const defaultEndpoint = "http://otel-collector.observability.svc.cluster.local:4317"

// Init sets up global OTLP/gRPC trace and metric providers for the given
// service name and returns a shutdown func that flushes and stops them.
//
// Behaviour:
//   - OTEL_EXPORTER_OTLP_ENDPOINT empty -> telemetry disabled, returns a
//     no-op shutdown and nil error.
//   - endpoint set -> batch TracerProvider + periodic MeterProvider are
//     installed as the global providers; the returned func tears both down.
func Init(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if endpoint == "" {
		// Telemetry disabled: nothing installed, shutdown is a no-op.
		return func(context.Context) error { return nil }, nil
	}

	// otlp*grpc exporters expect a host:port without the scheme. Strip a
	// leading http:// or https:// so the value from the env (and the
	// chart default) works either way. WithInsecure handles the lack of
	// TLS to the in-cluster collector.
	target := endpoint
	insecure := true
	switch {
	case strings.HasPrefix(target, "https://"):
		target = strings.TrimPrefix(target, "https://")
		insecure = false
	case strings.HasPrefix(target, "http://"):
		target = strings.TrimPrefix(target, "http://")
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	// --- traces ---
	traceOpts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(target)}
	if insecure {
		traceOpts = append(traceOpts, otlptracegrpc.WithInsecure())
	}
	traceExp, err := otlptracegrpc.New(ctx, traceOpts...)
	if err != nil {
		return nil, fmt.Errorf("otlp trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)

	// --- metrics ---
	metricOpts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(target)}
	if insecure {
		metricOpts = append(metricOpts, otlpmetricgrpc.WithInsecure())
	}
	metricExp, err := otlpmetricgrpc.New(ctx, metricOpts...)
	if err != nil {
		// Don't leak the trace provider if metrics fail to start.
		_ = tp.Shutdown(ctx)
		return nil, fmt.Errorf("otlp metric exporter: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
	)

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	shutdown := func(ctx context.Context) error {
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		tpErr := tp.Shutdown(shutdownCtx)
		mpErr := mp.Shutdown(shutdownCtx)
		if tpErr != nil {
			return tpErr
		}
		return mpErr
	}
	return shutdown, nil
}
