package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/integration"
)

func TestSoroForgeClient(t *testing.T) {
	t.Parallel()

	t.Run("paginates and sends bearer token", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer secret-token" {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid token"})
				return
			}
			assert.Equal(t, "testnet", r.URL.Query().Get("network"))

			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("cursor") == "" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"contracts": []integration.TrackedContract{
						{Name: "token", ContractID: "C1", Network: "testnet", WasmHash: "aa"},
					},
					"next_cursor": "page2",
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"contracts": []integration.TrackedContract{
					{Name: "vault", ContractID: "C2", Network: "testnet", WasmHash: "bb"},
				},
			})
		}))
		t.Cleanup(srv.Close)

		client, err := integration.NewSoroForge(srv.URL, "secret-token", srv.Client(), 5*time.Second)
		require.NoError(t, err)

		contracts, err := client.Contracts(context.Background(), "testnet")
		require.NoError(t, err)
		require.Len(t, contracts, 2)
		assert.Equal(t, "token", contracts[0].Name)
		assert.Equal(t, "vault", contracts[1].Name)
	})

	t.Run("surfaces authentication errors clearly", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "expired read-only token"})
		}))
		t.Cleanup(srv.Close)

		client, err := integration.NewSoroForge(srv.URL, "bad-token", srv.Client(), 5*time.Second)
		require.NoError(t, err)

		_, err = client.Contracts(context.Background(), "testnet")
		require.ErrorIs(t, err, integration.ErrUnauthorized)
		assert.Contains(t, err.Error(), "expired read-only token")
	})
}
