package probe_test

import (
	"context"
	"math/big"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/scval"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func setSimulateSpec(t *testing.T, fake *stellartest.Fake, fn string, spec string) {
	t.Helper()
	codec := scval.NewRegistry()
	val, err := codec.Encode(spec)
	require.NoError(t, err)
	valXDR, err := xdr.MarshalBase64(val)
	require.NoError(t, err)

	base := stellartest.LoadSimulate(t, "simulate_success.json")
	if len(base.Results) > 0 {
		base.Results[0].ReturnValueXDR = &valXDR
	}
	fake.Simulations[fn] = base
}

func TestBalances(t *testing.T) {
	t.Parallel()

	t.Run("reads decimals, symbol and balances for accounts and contracts", func(t *testing.T) {
		t.Parallel()

		fake := stellartest.NewFake(t)
		// decimals() is already recorded as 7 in NewFake.
		setSimulateSpec(t, fake, "symbol", `str:"USDC"`)
		setSimulateSpec(t, fake, "balance", "i128:125000000")

		p := newProber(t, fake, health.DefaultThresholds)
		res, err := p.Balances(context.Background(), probe.BalanceRequest{
			TokenID: stellartest.SACContract,
			Holders: []string{
				stellartest.SourceAccount,
				stellartest.WasmContract,
			},
		})
		require.NoError(t, err)
		assert.Equal(t, uint32(7), res.Decimals)
		assert.Equal(t, "USDC", res.Symbol)
		require.Len(t, res.Balances, 2)
		assert.Equal(t, stellartest.SourceAccount, res.Balances[0].Address)
		assert.Equal(t, "125000000", res.Balances[0].Raw)
		assert.Equal(t, "12.5", res.Balances[0].Formatted)
		assert.Equal(t, stellartest.WasmContract, res.Balances[1].Address)
		assert.Equal(t, "12.5", res.Balances[1].Formatted)
	})

	t.Run("FormatTokenAmount handles zero, whole and fractional values", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "0.0", probe.FormatTokenAmount(big.NewInt(0), 7))
		assert.Equal(t, "10.0", probe.FormatTokenAmount(big.NewInt(100_000_000), 7))
		assert.Equal(t, "0.0000001", probe.FormatTokenAmount(big.NewInt(1), 7))
		assert.Equal(t, "42", probe.FormatTokenAmount(big.NewInt(42), 0))
	})
}
