package probe_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func TestCompare(t *testing.T) {
	t.Parallel()

	t.Run("matching WASM and call results across two networks", func(t *testing.T) {
		t.Parallel()

		leftFake := stellartest.NewFake(t)
		rightFake := stellartest.NewFake(t)
		leftProber := newProber(t, leftFake, health.DefaultThresholds)
		rightProber := newProber(t, rightFake, health.DefaultThresholds)

		res, err := probe.Compare(context.Background(), probe.CompareRequest{
			Left: probe.CompareTarget{
				Name:       "testnet",
				ContractID: stellartest.WasmContract,
				Prober:     leftProber,
			},
			Right: probe.CompareTarget{
				Name:       "mainnet",
				ContractID: stellartest.WasmContract,
				Prober:     rightProber,
			},
			Calls: []probe.CompareCall{
				{Function: "decimals"},
			},
		})
		require.NoError(t, err)
		assert.True(t, res.WasmMatch)
		assert.Empty(t, res.Differences)
		require.Len(t, res.Entries, 2)
		require.Len(t, res.Calls, 1)
		assert.True(t, res.Calls[0].Match)
	})

	t.Run("detects WASM mismatch and TTL difference", func(t *testing.T) {
		t.Parallel()

		leftFake := stellartest.NewFake(t)
		rightFake := stellartest.NewFake(t)
		instanceKey := stellartest.InstanceKeyOf(t, stellartest.SACContract)
		rightFake.SetTTL(t, instanceKey, rightFake.LatestLedger+50_000)

		leftProber := newProber(t, leftFake, health.DefaultThresholds)
		rightProber := newProber(t, rightFake, health.DefaultThresholds)

		res, err := probe.Compare(context.Background(), probe.CompareRequest{
			Left: probe.CompareTarget{
				Name:       "testnet",
				ContractID: stellartest.WasmContract,
				Prober:     leftProber,
			},
			Right: probe.CompareTarget{
				Name:       "mainnet",
				ContractID: stellartest.SACContract,
				Prober:     rightProber,
			},
		})
		require.NoError(t, err)
		assert.False(t, res.WasmMatch)
		assert.NotEmpty(t, res.Differences)
	})
}
