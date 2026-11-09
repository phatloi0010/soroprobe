package probe_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/abi"
	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

type staticABISource struct {
	iface *abi.Interface
}

func (s *staticABISource) Interface(context.Context, string, string) (*abi.Interface, error) {
	if s.iface == nil {
		return nil, abi.ErrUnknownContract
	}
	return s.iface, nil
}

func TestCheckUpgradeability(t *testing.T) {
	t.Parallel()

	t.Run("detects upgrade function, admin and code last modified ledger", func(t *testing.T) {
		t.Parallel()

		fake := stellartest.NewFake(t)
		src := &staticABISource{
			iface: &abi.Interface{
				Functions: []abi.Function{
					{
						Name: "upgrade",
						Inputs: []abi.Param{
							{Name: "new_wasm_hash", Type: abi.Type{Kind: "bytes_n", Display: "BytesN<32>"}},
						},
					},
					{
						Name:    "admin",
						Outputs: []abi.Type{{Kind: "address", Display: "Address"}},
					},
				},
			},
		}

		p, err := probe.New(probe.Options{
			Client:        fake,
			SourceAccount: stellartest.SourceAccount,
			Thresholds:    health.DefaultThresholds,
			ABI:           src,
			Network:       "testnet",
		})
		require.NoError(t, err)

		res, err := p.Check(context.Background(), probe.CheckRequest{
			ContractID:     stellartest.WasmContract,
			Upgradeability: true,
		})
		require.NoError(t, err)
		assert.True(t, res.OK)
		assert.Equal(t, probe.OutcomePass, outcomes(res)["upgradeability"])

		require.NotNil(t, res.Upgradeability)
		assert.True(t, res.Upgradeability.Upgradeable)
		assert.Equal(t, []string{"fn upgrade(new_wasm_hash: BytesN<32>)"}, res.Upgradeability.Functions)
		assert.NotZero(t, res.Upgradeability.CodeLastModifiedLedger)
		assert.Contains(t, res.Upgradeability.HeuristicNote, "best-effort heuristic")
	})

	t.Run("reports non-upgradeable when no upgrade function exists", func(t *testing.T) {
		t.Parallel()

		fake := stellartest.NewFake(t)
		p := newProber(t, fake, health.DefaultThresholds)

		res, err := p.Check(context.Background(), probe.CheckRequest{
			ContractID:     stellartest.WasmContract,
			Upgradeability: true,
		})
		require.NoError(t, err)
		require.NotNil(t, res.Upgradeability)
		assert.False(t, res.Upgradeability.Upgradeable)
		assert.NotZero(t, res.Upgradeability.CodeLastModifiedLedger)
		assert.Contains(t, res.Upgradeability.HeuristicNote, "best-effort heuristic")
	})

	t.Run("reports stellar asset contract as built into host", func(t *testing.T) {
		t.Parallel()

		fake := stellartest.NewFake(t)
		p := newProber(t, fake, health.DefaultThresholds)

		res, err := p.Check(context.Background(), probe.CheckRequest{
			ContractID:     stellartest.SACContract,
			Upgradeability: true,
		})
		require.NoError(t, err)
		require.NotNil(t, res.Upgradeability)
		assert.False(t, res.Upgradeability.Upgradeable)
		assert.Contains(t, res.Upgradeability.HeuristicNote, "Stellar Asset Contracts")
	})
}
