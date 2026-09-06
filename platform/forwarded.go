package platform

import (
	"context"
	"net"
	"net/http"
)

// ForwardedForHeader carries the address of the party that reached the SERVICE
// making this call, so the platform limits and records the real client rather
// than the service standing in front of it.
const ForwardedForHeader = "X-Forwarded-For"

type originatingClientIPKey struct{}

// WithOriginatingClientIP marks a context as belonging to one inbound request,
// so every platform call made under it says who caused it. A gateway sets this
// ONCE where it admits a request; nothing below has to thread it through.
//
// The value must be the peer the service itself observed - never a header the
// client supplied. A forwarded address decides a rate-limit bucket, so accepting
// the client's own would let any caller choose its bucket and exhaust another's.
// An address that does not parse is dropped rather than sent.
func WithOriginatingClientIP(ctx context.Context, ip string) context.Context {
	if ip == "" {
		return ctx
	}
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	if net.ParseIP(ip) == nil {
		return ctx
	}
	return context.WithValue(ctx, originatingClientIPKey{}, ip)
}

// OriginatingClientIP is the address WithOriginatingClientIP recorded, or "".
func OriginatingClientIP(ctx context.Context) string {
	ip, _ := ctx.Value(originatingClientIPKey{}).(string)
	return ip
}

// setForwardedFor states the originating client on one outbound request. It
// OVERWRITES rather than appends: this SDK sends one hop's worth of truth, and
// the platform honours it only from an address its own trusted-proxy list names.
func setForwardedFor(req *http.Request, ctx context.Context) {
	if ip := OriginatingClientIP(ctx); ip != "" {
		req.Header.Set(ForwardedForHeader, ip)
	}
}
