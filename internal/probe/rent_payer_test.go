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

func TestCheckRentPayer(t *testing.T) {
	t.Parallel()

	const payer = stellartest.SourceAccount

	t.Run("passes when spendable balance comfortably covers expiring entry rent", func(t *testing.T) {
		t.Parallel()

		fake := stellartest.NewFake(t)
		instanceKey := stellartest.InstanceKeyOf(t, stellartest.SACContract)
		fake.SetTTL(t, instanceKey, fake.LatestLedger+50_000) // warning band
		// 2 base reserves (10_000_000) + 1 subentry (5_000_000) = 15_000_000 reserve.
		// Balance 20_000_000 -> 5_000_000 spendable, which is >> 2 * 6128.
		fake.SetAccount(t, payer, 20_000_000, 1)

		p := newProber(t, fake, health.DefaultThresholds)
		res, err := p.Check(context.Background(), probe.CheckRequest{
			ContractID: stellartest.SACContract,
			RentPayer:  payer,
		})
		require.NoError(t, err)
		assert.True(t, res.OK)
		assert.Equal(t, probe.OutcomePass, outcomes(res)["rent_payer"])
		require.NotNil(t, res.RentPayer)
		assert.True(t, res.RentPayer.Exists)
		assert.Equal(t, int64(20_000_000), res.RentPayer.BalanceStroops)
		assert.Equal(t, int64(15_000_000), res.RentPayer.ReserveStroops)
		assert.Equal(t, int64(5_000_000), res.RentPayer.SpendableStroops)
		assert.Equal(t, 1, res.RentPayer.ExpiringEntries)
		assert.Equal(t, int64(extendFee), res.RentPayer.EstimatedRent)
	})

	t.Run("warns when spendable balance is less than 2x estimated rent", func(t *testing.T) {
		t.Parallel()

		fake := stellartest.NewFake(t)
		instanceKey := stellartest.InstanceKeyOf(t, stellartest.SACContract)
		fake.SetTTL(t, instanceKey, fake.LatestLedger+50_000)
		// 2 base reserves = 10_000_000. Balance = 10_000_000 + extendFee (barely 1x).
		fake.SetAccount(t, payer, 10_000_000+extendFee, 0)

		p := newProber(t, fake, health.DefaultThresholds)
		res, err := p.Check(context.Background(), probe.CheckRequest{
			ContractID: stellartest.SACContract,
			RentPayer:  payer,
		})
		require.NoError(t, err)
		assert.True(t, res.OK)
		assert.Equal(t, probe.OutcomeWarn, outcomes(res)["rent_payer"])
	})

	t.Run("fails when spendable balance is below estimated rent", func(t *testing.T) {
		t.Parallel()

		fake := stellartest.NewFake(t)
		instanceKey := stellartest.InstanceKeyOf(t, stellartest.SACContract)
		fake.SetTTL(t, instanceKey, fake.LatestLedger+50_000)
		// 2 base reserves = 10_000_000. Balance = 10_001_000 -> 1_000 spendable < 6128.
		fake.SetAccount(t, payer, 10_001_000, 0)

		p := newProber(t, fake, health.DefaultThresholds)
		res, err := p.Check(context.Background(), probe.CheckRequest{
			ContractID: stellartest.SACContract,
			RentPayer:  payer,
		})
		require.NoError(t, err)
		assert.False(t, res.OK)
		assert.Equal(t, probe.OutcomeFail, outcomes(res)["rent_payer"])
	})

	t.Run("fails when rent payer account does not exist", func(t *testing.T) {
		t.Parallel()

		fake := stellartest.NewFake(t)
		p := newProber(t, fake, health.DefaultThresholds)
		res, err := p.Check(context.Background(), probe.CheckRequest{
			ContractID: stellartest.SACContract,
			RentPayer:  payer,
		})
		require.NoError(t, err)
		assert.False(t, res.OK)
		assert.Equal(t, probe.OutcomeFail, outcomes(res)["rent_payer"])
		require.NotNil(t, res.RentPayer)
		assert.False(t, res.RentPayer.Exists)
	})
}
