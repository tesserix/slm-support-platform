// Package observability wires OpenTelemetry traces and metrics for otto.
//
// Init configures OTLP/gRPC exporters pointed at the in-cluster collector and
// installs global Tracer/Meter providers. It is a no-op when the OTLP endpoint
// env var is empty, so local dev and tests run without a collector.
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
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// defaultEndpoint is the in-cluster OTLP/gRPC collector address.
const defaultEndpoint = "http://otel-collector.observability.svc.cluster.local:4317"

// Init installs global trace + metric providers exporting over OTLP/gRPC.
//
// The endpoint is read from OTEL_EXPORTER_OTLP_ENDPOINT, defaulting to the
// in-cluster collector. If that env var is explicitly set to empty, OTel is
// disabled and Init returns a no-op shutdown func. The returned func flushes
// and releases exporters and should be deferred in main.
func Init(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }

	endpoint, set := os.LookupEnv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if !set {
		endpoint = defaultEndpoint
	}
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		// Explicitly disabled.
		return noop, nil
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(serviceName)),
	)
	if err != nil {
		return noop, fmt.Errorf("otel: build resource: %w", err)
	}

	// otlp*grpc dial options. WithEndpoint expects host:port without a
	// scheme; strip http:// or https:// and treat the latter as TLS-on.
	traceOpts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(stripScheme(endpoint))}
	metricOpts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(stripScheme(endpoint))}
	if !strings.HasPrefix(endpoint, "https://") {
		traceOpts = append(traceOpts, otlptracegrpc.WithInsecure())
		metricOpts = append(metricOpts, otlpmetricgrpc.WithInsecure())
	}

	traceExp, err := otlptracegrpc.New(ctx, traceOpts...)
	if err != nil {
		return noop, fmt.Errorf("otel: trace exporter: %w", err)
	}

	metricExp, err := otlpmetricgrpc.New(ctx, metricOpts...)
	if err != nil {
		_ = traceExp.Shutdown(ctx)
		return noop, fmt.Errorf("otel: metric exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithInterval(30*time.Second))),
		sdkmetric.WithResource(res),
	)

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	shutdown := func(ctx context.Context) error {
		err := tp.Shutdown(ctx)
		if merr := mp.Shutdown(ctx); merr != nil && err == nil {
			err = merr
		}
		return err
	}
	return shutdown, nil
}

// stripScheme removes an http:// or https:// prefix so the grpc exporter
// receives a bare host:port authority.
func stripScheme(endpoint string) string {
	endpoint = strings.TrimPrefix(endpoint, "http://")
	endpoint = strings.TrimPrefix(endpoint, "https://")
	return endpoint
}
