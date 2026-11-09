package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/config"
	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/integration"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func TestCheckFromSoroForge(t *testing.T) {
	fake := stellartest.NewFake(t)
	p, err := probe.New(probe.Options{
		Client:        fake,
		SourceAccount: stellartest.SourceAccount,
		Thresholds:    health.DefaultThresholds,
	})
	require.NoError(t, err)
	inspect, err := p.Inspect(context.Background(), probe.InspectRequest{ContractID: stellartest.WasmContract})
	require.NoError(t, err)
	liveHash := inspect.WasmHash

	t.Run("passes when live WASM matches SoroForge hash", func(t *testing.T) {
		t.Chdir(t.TempDir())
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer ro-token", r.Header.Get("Authorization"))
			assert.Equal(t, "testnet", r.URL.Query().Get("network"))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"contracts": []integration.TrackedContract{
					{
						Name:       "vault",
						ContractID: stellartest.WasmContract,
						Network:    "testnet",
						WasmHash:   liveHash,
					},
				},
			})
		}))
		t.Cleanup(srv.Close)

		cmd, opts := newRootCmd()
		opts.newProber = func(config.Config) (*probe.Prober, func(), error) {
			return p, func() {}, nil
		}

		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{
			"check",
			"--from-soroforge", srv.URL,
			"--soroforge-token", "ro-token",
			"--network", "testnet",
		})

		require.NoError(t, cmd.Execute())
		assert.Contains(t, out.String(), "PASS")
	})

	t.Run("fails and reports drift when WASM hash differs", func(t *testing.T) {
		t.Chdir(t.TempDir())
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"contracts": []integration.TrackedContract{
					{
						Name:       "vault",
						ContractID: stellartest.WasmContract,
						Network:    "testnet",
						WasmHash:   "0000000000000000000000000000000000000000000000000000000000000000",
					},
				},
			})
		}))
		t.Cleanup(srv.Close)

		cmd, opts := newRootCmd()
		opts.newProber = func(config.Config) (*probe.Prober, func(), error) {
			return p, func() {}, nil
		}

		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{
			"check",
			"--from-soroforge", srv.URL,
			"--network", "testnet",
		})

		err := cmd.Execute()
		require.Error(t, err)
		var failed *checkFailedError
		assert.ErrorAs(t, err, &failed)
		assert.Contains(t, out.String(), "WASM drift detected")
	})
}
