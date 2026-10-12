package telemetry

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupDisabledReturnsBase(t *testing.T) {
	t.Setenv(envEndpoint, "")
	t.Setenv(envLogsEndpoint, "")

	h, shutdown, err := Setup(context.Background(), slog.DiscardHandler, Opts{ServiceName: "svc"})
	require.NoError(t, err)

	assert.Equal(t, slog.DiscardHandler, h)
	assert.NoError(t, shutdown(context.Background()))
}

func TestSetupForwardsLogsLikeBase(t *testing.T) {
	var (
		mu    sync.Mutex
		paths []string
		logs  bytes.Buffer
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/v1/logs" {
			logs.Write(body)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv(envEndpoint, srv.URL)
	t.Setenv(envLogsEndpoint, "")

	var stderr bytes.Buffer
	base := slog.NewTextHandler(&stderr, &slog.HandlerOptions{Level: slog.LevelInfo})

	h, shutdown, err := Setup(context.Background(), base, Opts{ServiceName: "svc", Version: "test"})
	require.NoError(t, err)

	lo := slog.New(h).With("component", "svc")
	lo.Info("visible line", "k", "v")
	lo.Debug("hidden line")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, shutdown(ctx))

	mu.Lock()
	defer mu.Unlock()

	assert.Contains(t, stderr.String(), "visible line", "local handler keeps working")
	assert.Contains(t, paths, "/v1/logs")
	assert.Contains(t, paths, "/v1/metrics", "shutdown flushes metrics")
	assert.True(t, strings.Contains(logs.String(), "visible line"), "info line reaches the collector")
	assert.False(t, strings.Contains(logs.String(), "hidden line"), "debug is dropped, same as stderr")
}
