package tracing

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/disciplinedware/declarion-sdk-go/errs"
	"github.com/felixge/httpsnoop"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const InstrumentationName = "github.com/disciplinedware/declarion-sdk-go/tracing"

func knownHTTPMethods() map[string]bool {
	methods := []string{http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPatch, http.MethodPost, http.MethodPut, http.MethodTrace, "QUERY"}
	if configured, exists := os.LookupEnv("OTEL_INSTRUMENTATION_HTTP_KNOWN_METHODS"); exists {
		methods = strings.Split(configured, ",")
	}
	known := make(map[string]bool, len(methods))
	for _, method := range methods {
		if method != "" {
			known[method] = true
		}
	}
	return known
}

func httpMethod(method string, known map[string]bool) (string, string) {
	if known[method] {
		return method, method
	}
	return "_OTHER", "HTTP"
}

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
	if errors.As(err, &ne) {
		if ne.Timeout() {
			return "timeout"
		}
		return "transport"
	}
	return "_OTHER"
}

func ErrorType(err error) string {
	if declared, ok := errs.From(err); ok && errs.Declared(declared.Code()) {
		return declared.Code()
	}
	return TransportError(err)
}

// ServerMiddleware's route callback must return a declared template, never a raw path.
func ServerMiddleware(route func(*http.Request) string, skip func(*http.Request) bool) func(http.Handler) http.Handler {
	known := knownHTTPMethods()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if skip != nil && skip(r) {
				next.ServeHTTP(w, r)
				return
			}
			method, name := httpMethod(r.Method, known)
			ctx := WithWork(ExtractTrace(r.Context(), r.Header), "", "http:"+name)
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			ctx, span := otel.Tracer(InstrumentationName).Start(ctx, name, trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(attribute.String("http.request.method", method), attribute.String("url.scheme", scheme)))
			defer span.End()
			request := r.WithContext(ctx)
			metrics := httpsnoop.CaptureMetrics(next, w, request)
			span.SetAttributes(attribute.Int("http.response.status_code", metrics.Code))
			if metrics.Code >= 500 {
				Fail(span, strconv.Itoa(metrics.Code))
			}
			if route != nil {
				if template := route(request); template != "" {
					span.SetName(name + " " + template)
					span.SetAttributes(attribute.String("http.route", template))
				}
			}
		})
	}
}

type transport struct {
	base     http.RoundTripper
	origin   *url.URL
	template func(*url.URL) string
	methods  map[string]bool
}

func (t *transport) Unwrap() http.RoundTripper { return t.base }

type HTTPClientOption func(*transport)

func WithURLTemplateLookup(lookup func(*url.URL) string) HTTPClientOption {
	return func(t *transport) { t.template = lookup }
}

func HTTPClient(client *http.Client, platformURL string, options ...HTTPClientOption) *http.Client {
	copyClient := *client
	base := copyClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	var origin *url.URL
	if platformURL != "" {
		origin, _ = url.Parse(platformURL)
	}
	wrapper := &transport{base: base, origin: origin, methods: knownHTTPMethods()}
	for _, option := range options {
		option(wrapper)
	}
	copyClient.Transport = wrapper
	return &copyClient
}

func sameOrigin(a, b *url.URL) bool {
	if !strings.EqualFold(a.Scheme, b.Scheme) || effectivePort(a) != effectivePort(b) {
		return false
	}
	aHost, bHost := a.Hostname(), b.Hostname()
	if aIP, bIP := net.ParseIP(aHost), net.ParseIP(bHost); aIP != nil && bIP != nil {
		return aIP.Equal(bIP)
	}
	return strings.EqualFold(aHost, bHost)
}

func effectivePort(destination *url.URL) string {
	if port := destination.Port(); port != "" {
		if n, err := strconv.Atoi(port); err == nil {
			return strconv.Itoa(n)
		}
		return port
	}
	if strings.EqualFold(destination.Scheme, "https") {
		return "443"
	}
	return "80"
}

func DestinationAttributes(destination *url.URL) []attribute.KeyValue {
	attrs := []attribute.KeyValue{attribute.String("server.address", destination.Hostname())}
	port := effectivePort(destination)
	if n, err := strconv.Atoi(port); err == nil {
		attrs = append(attrs, attribute.Int("server.port", n))
	}
	return attrs
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	method, name := httpMethod(req.Method, t.methods)
	attrs := append(DestinationAttributes(req.URL), attribute.String("http.request.method", method))
	if t.template != nil && t.origin != nil && sameOrigin(t.origin, req.URL) {
		if template := t.template(req.URL); template != "" {
			name += " " + template
			attrs = append(attrs, attribute.String("url.template", template))
		}
	}
	ctx, span := otel.Tracer(InstrumentationName).Start(req.Context(), name, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
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
