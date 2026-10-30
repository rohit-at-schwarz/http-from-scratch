package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"httpServer/internal/myHttp"
)

var tracer = otel.Tracer("httpServer/cmd/httpserver")

func setupTracing(ctx context.Context) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	if os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}

	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create trace exporter: %w", err)
	}

	//OTEL_SERVICE_NAME / OTEL_RESOURCE_ATTRIBUTES can override the default name
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName("my-http-server")),
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		return nil, fmt.Errorf("build trace resource: %w", err)
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(provider)

	return provider.Shutdown, nil
}

func TracingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))

		method := myHttp.MethodLabel(r.Method)
		route := RouteLabel(r)

		spanName := method
		attrs := []attribute.KeyValue{
			semconv.HTTPRequestMethodKey.String(method),
			semconv.URLPath(r.URL.Path),
		}
		if route != "unmatched" {
			spanName = method + " " + route
			attrs = append(attrs, semconv.HTTPRoute(route))
		}

		ctx, span := tracer.Start(ctx, spanName,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attrs...),
		)
		defer span.End()

		interceptor := myHttp.NewResponseWriterInterceptor(w)
		next.ServeHTTP(interceptor, r.WithContext(ctx))

		span.SetAttributes(semconv.HTTPResponseStatusCode(interceptor.StatusCode))
		//4xx is the clients mistake so only 5xx marks a server span as failed
		if interceptor.StatusCode >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(interceptor.StatusCode))
		}
	})
}

func endSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}
