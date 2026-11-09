package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/health"
)

func TestColorEnabledRespectsNoColor(t *testing.T) {
	t.Run("empty NO_COLOR disables color", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		assert.False(t, colorEnabled(os.Stdout))
	})

	t.Run("non-empty NO_COLOR disables color", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		assert.False(t, colorEnabled(os.Stdout))
	})
}

func TestColorEnabledNonTerminalWriters(t *testing.T) {
	t.Run("non-file writer is never colored", func(t *testing.T) {
		var buf bytes.Buffer
		assert.False(t, colorEnabled(&buf))
	})

	t.Run("regular file is not a character device and is not colored", func(t *testing.T) {
		tmp, err := os.CreateTemp(t.TempDir(), "soroprobe-color-*")
		require.NoError(t, err)
		defer func() { _ = tmp.Close() }()

		assert.False(t, colorEnabled(tmp))
	})

	t.Run("closed file stat error is not colored", func(t *testing.T) {
		tmp, err := os.CreateTemp(t.TempDir(), "soroprobe-closed-*")
		require.NoError(t, err)
		require.NoError(t, tmp.Close())

		assert.False(t, colorEnabled(tmp))
	})
}

func TestStylerStatusRendering(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    health.Status
		wantLabel string
		wantPlain string
		wantANSI  string
	}{
		{
			name:      "healthy",
			status:    health.StatusHealthy,
			wantLabel: "OK",
			wantPlain: "OK       healthy",
			wantANSI:  ansiGreen,
		},
		{
			name:      "warning",
			status:    health.StatusWarning,
			wantLabel: "WARN",
			wantPlain: "WARN     expiring soon",
			wantANSI:  ansiYellow,
		},
		{
			name:      "critical",
			status:    health.StatusCritical,
			wantLabel: "CRITICAL",
			wantPlain: "CRITICAL expiring very soon",
			wantANSI:  ansiRed,
		},
		{
			name:      "expired",
			status:    health.StatusExpired,
			wantLabel: "EXPIRED",
			wantPlain: "EXPIRED  ttl has lapsed",
			wantANSI:  ansiRed,
		},
		{
			name:      "missing",
			status:    health.StatusMissing,
			wantLabel: "MISSING",
			wantPlain: "MISSING  not on ledger",
			wantANSI:  ansiRed,
		},
		{
			name:      "unknown",
			status:    health.StatusUnknown,
			wantLabel: "UNKNOWN",
			wantPlain: "UNKNOWN  no ttl reported",
			wantANSI:  ansiDim,
		},
	}

	plain := styler{on: false}
	colored := styler{on: true}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotPlain := plain.status(tt.status)
			assert.Equal(t, tt.wantPlain, gotPlain)
			assert.True(t, strings.HasPrefix(gotPlain, tt.wantLabel))
			assert.NotContains(t, gotPlain, "\033[")

			gotColored := colored.status(tt.status)
			assert.Equal(t, tt.wantANSI+tt.wantPlain+ansiReset, gotColored)
			assert.True(t, strings.HasPrefix(gotColored, tt.wantANSI))
			assert.True(t, strings.HasSuffix(gotColored, ansiReset))
			assert.Contains(t, gotColored, tt.wantLabel)
		})
	}
}
