package tracing

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"

	"github.com/felixge/httpsnoop"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const InstrumentationName = "github.com/disciplinedware/declarion-sdk-go/tracing"

func Fail(span trace.Span, errorType string) {
	span.SetAttributes(attribute.String("error.type", errorType))
	span.SetStatus(codes.Error, "")
}

func TransportError(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	return "transport"
}

// ServerMiddleware's route callback must return a declared template, never a raw path.
func ServerMiddleware(route func(*http.Request) string, skip func(*http.Request) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if skip != nil && skip(r) {
				next.ServeHTTP(w, r)
				return
			}
			ctx := WithWork(ExtractTrace(r.Context(), r.Header), "", "http:"+r.Method)
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			ctx, span := otel.Tracer(InstrumentationName).Start(ctx, r.Method, trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(attribute.String("http.request.method", r.Method), attribute.String("url.scheme", scheme)))
			defer span.End()
			request := r.WithContext(ctx)
			metrics := httpsnoop.CaptureMetrics(next, w, request)
			span.SetAttributes(attribute.Int("http.response.status_code", metrics.Code))
			if metrics.Code >= 500 {
				Fail(span, strconv.Itoa(metrics.Code))
			}
			if route != nil {
				if template := route(request); template != "" {
					span.SetName(r.Method + " " + template)
					span.SetAttributes(attribute.String("http.route", template))
				}
			}
		})
	}
}

type transport struct {
	base   http.RoundTripper
	origin *url.URL
}

func (t *transport) Unwrap() http.RoundTripper { return t.base }

func HTTPClient(client *http.Client, platformURL string) *http.Client {
	copyClient := *client
	base := copyClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	var origin *url.URL
	if platformURL != "" {
		origin, _ = url.Parse(platformURL)
	}
	copyClient.Transport = &transport{base: base, origin: origin}
	return &copyClient
}

func sameOrigin(a, b *url.URL) bool { return a.Scheme == b.Scheme && a.Host == b.Host }

func DestinationAttributes(destination *url.URL) []attribute.KeyValue {
	attrs := []attribute.KeyValue{attribute.String("server.address", destination.Hostname())}
	port := destination.Port()
	if port == "" {
		if destination.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if n, err := strconv.Atoi(port); err == nil {
		attrs = append(attrs, attribute.Int("server.port", n))
	}
	return attrs
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	attrs := append(DestinationAttributes(req.URL), attribute.String("http.request.method", req.Method))
	ctx, span := otel.Tracer(InstrumentationName).Start(req.Context(), req.Method, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
	defer span.End()
	copyReq := req.Clone(ctx)
	if t.origin != nil {
		Strip(copyReq.Header)
		if sameOrigin(t.origin, req.URL) {
			Inject(ctx, copyReq.Header)
		}
	}
	response, err := t.base.RoundTrip(copyReq)
	if err != nil {
		Fail(span, TransportError(err))
	} else if response != nil {
		span.SetAttributes(attribute.Int("http.response.status_code", response.StatusCode))
		if response.StatusCode >= 400 {
			Fail(span, strconv.Itoa(response.StatusCode))
		}
	}
	return response, err
}

// PlatformClient preserves redirect decisions while stripping context when a protocol hop changes origin.
func PlatformClient(client *http.Client, destination *url.URL) *http.Client {
	copyClient := *client
	previous := client.CheckRedirect
	copyClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !sameOrigin(destination, req.URL) {
			Strip(req.Header)
		}
		if previous != nil {
			return previous(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &copyClient
}
