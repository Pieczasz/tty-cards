package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Pieczasz/tty-cards/internal/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/trace"
)

// Every visitor of the website polls this API, so a span per request put each
// visitor's address and User-Agent into Tempo. The request metrics stay; spans go.
//
//nolint:paralleltest // reads the process-global span recorder
func TestNewServer_ProducesNoSpans(t *testing.T) {
	h := handler(t, Deps{RequestsPerMinute: 10, Users: &stubUsers{rankings: []db.Ranking{ranking("ana", "Poker", 1600)}}})

	for _, target := range []string{"/v1/stats", "/v1/leaderboard", "/v1/leaderboard?limit=200", "/healthz"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.RemoteAddr = "203.0.113.50:5555"
		req.Header.Set("User-Agent", "Mozilla/5.0 (a visitor)")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}

	assert.Empty(t, testSpans.Started(), "the stats API must not produce spans")
}

// ctxUsers keeps the context the leaderboard handler reached the repository with.
type ctxUsers struct {
	stubUsers
	ctx context.Context //nolint:containedctx // the context under test
}

func (c *ctxUsers) BestPlayers(ctx context.Context, game string, limit int) ([]db.Ranking, error) {
	c.ctx = ctx
	return c.stubUsers.BestPlayers(ctx, game, limit)
}

// otelhttp extracted a visitor's traceparent into the request context even with a
// noop tracer, so the repository's leaderboard span became a child of it and Tempo
// stored a trace under an id the visitor chose.
//
//nolint:paralleltest // reads the process-global span recorder
func TestNewServer_IgnoresAVisitorsTraceContext(t *testing.T) {
	users := &ctxUsers{}
	h := handler(t, Deps{Users: users})

	req := httptest.NewRequest(http.MethodGet, "/v1/leaderboard", nil)
	req.RemoteAddr = "203.0.113.52:5555"
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, users.ctx, "the leaderboard was never read")
	assert.False(t, trace.SpanContextFromContext(users.ctx).IsValid(),
		"the visitor's traceparent reached the repository")
	assert.Empty(t, testSpans.Started())
}

// httpServerAttributeKeys is the whole vocabulary otelhttp's http.server.* metrics may
// carry here. Every value behind these keys is bounded: the method is normalised to a
// known verb or _OTHER, the route is a registered mux pattern, and server.address is
// pinned by WithServerName rather than read from the client's Host header.
var httpServerAttributeKeys = map[string]bool{
	"http.request.method": true, "http.response.status_code": true, "http.route": true,
	"url.scheme": true, "server.address": true,
	"network.protocol.name": true, "network.protocol.version": true,
}

// internal/observability's allow-list covers the app's own instruments; otelhttp's are
// registered here, so they are checked here. A client-chosen Host port used to become
// server.port - up to 65535 series from one header.
//
//nolint:paralleltest // reads the process-global manual reader
func TestNewServer_HTTPMetricsCarryBoundedAttributesOnly(t *testing.T) {
	h := handler(t, Deps{RequestsPerMinute: 10})

	for _, target := range []string{"/v1/stats", "/v1/invented/" + strings.Repeat("x", 40)} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Host = "attacker.example:4242"
		req.RemoteAddr = "203.0.113.51:5555"
		h.ServeHTTP(httptest.NewRecorder(), req)
	}

	var got metricdata.ResourceMetrics
	require.NoError(t, testMetrics.Collect(t.Context(), &got))

	seen := 0
	for _, scope := range got.ScopeMetrics {
		for _, m := range scope.Metrics {
			if !strings.HasPrefix(m.Name, "http.server.") {
				continue
			}
			seen++
			for _, set := range attributeSets(m) {
				for _, kv := range set.ToSlice() {
					assert.Truef(t, httpServerAttributeKeys[string(kv.Key)],
						"metric %q carries attribute %q=%q, which is not on the allow-list",
						m.Name, kv.Key, kv.Value.String())
					if kv.Key == "server.address" {
						assert.Equal(t, "stats-api", kv.Value.AsString(), "server.address must not come from the Host header")
					}
				}
			}
		}
	}
	assert.Positive(t, seen, "no http.server.* instrument was recorded")
}

func attributeSets(m metricdata.Metrics) []attribute.Set {
	var out []attribute.Set
	switch data := m.Data.(type) {
	case metricdata.Sum[int64]:
		for _, dp := range data.DataPoints {
			out = append(out, dp.Attributes)
		}
	case metricdata.Histogram[int64]:
		for _, dp := range data.DataPoints {
			out = append(out, dp.Attributes)
		}
	case metricdata.Histogram[float64]:
		for _, dp := range data.DataPoints {
			out = append(out, dp.Attributes)
		}
	}
	return out
}
