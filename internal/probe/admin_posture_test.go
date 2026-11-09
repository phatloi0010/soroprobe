package probe_test

import (
	"context"
	"testing"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/scval"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func setSimulateReturn(t *testing.T, fake *stellartest.Fake, fn string, spec string) {
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

func makeSigner(t *testing.T, accountID string, weight uint32) xdr.Signer {
	t.Helper()
	raw, err := strkey.Decode(strkey.VersionByteAccountID, accountID)
	require.NoError(t, err)
	var ed xdr.Uint256
	copy(ed[:], raw)
	return xdr.Signer{
		Key: xdr.SignerKey{
			Type:    xdr.SignerKeyTypeSignerKeyTypeEd25519,
			Ed25519: &ed,
		},
		Weight: xdr.Uint32(weight),
	}
}

func TestCheckAdminPosture(t *testing.T) {
	t.Parallel()

	const adminAcct = stellartest.SourceAccount
	const secondSigner = "GB32JP5GNOQLW4NUMV6H2UBOSMB4DROL7YNDBWAHYXMVFLLJOH3LXKHK"

	t.Run("warns on single-signer account admin with master weight and highlights mainnet", func(t *testing.T) {
		t.Parallel()

		fake := stellartest.NewFake(t)
		setSimulateReturn(t, fake, "admin", "addr:"+adminAcct)
		fake.SetAccountSigners(t, adminAcct, [4]byte{1, 0, 0, 0}, nil)

		p, err := probe.New(probe.Options{
			Client:        fake,
			SourceAccount: stellartest.SourceAccount,
			Thresholds:    health.DefaultThresholds,
			Network:       "public",
		})
		require.NoError(t, err)

		res, err := p.Check(context.Background(), probe.CheckRequest{
			ContractID: stellartest.SACContract,
			AdminFn:    "admin",
		})
		require.NoError(t, err)
		assert.True(t, res.OK)
		assert.Equal(t, probe.OutcomeWarn, outcomes(res)["admin_posture"])
		require.NotNil(t, res.Admin)
		assert.Equal(t, adminAcct, res.Admin.Address)
		assert.Equal(t, "account", res.Admin.Kind)
		assert.True(t, res.Admin.SingleKey)
		assert.True(t, res.Admin.MainnetRisk)
		assert.Equal(t, uint8(1), res.Admin.MasterWeight)
		assert.Equal(t, 1, res.Admin.SignerCount)

		var detail string
		for _, c := range res.Checks {
			if c.Name == "admin_posture" {
				detail = c.Detail
			}
		}
		assert.Contains(t, detail, "MAINNET SINGLE-KEY ADMIN")
	})

	t.Run("passes on multisig account admin", func(t *testing.T) {
		t.Parallel()

		fake := stellartest.NewFake(t)
		setSimulateReturn(t, fake, "admin", "addr:"+adminAcct)
		fake.SetAccountSigners(t, adminAcct, [4]byte{1, 1, 2, 2}, []xdr.Signer{
			makeSigner(t, secondSigner, 1),
		})

		p := newProber(t, fake, health.DefaultThresholds)
		res, err := p.Check(context.Background(), probe.CheckRequest{
			ContractID: stellartest.SACContract,
			AdminFn:    "admin",
		})
		require.NoError(t, err)
		assert.True(t, res.OK)
		assert.Equal(t, probe.OutcomePass, outcomes(res)["admin_posture"])
		require.NotNil(t, res.Admin)
		assert.False(t, res.Admin.SingleKey)
		assert.Equal(t, 2, res.Admin.SignerCount)
		assert.Equal(t, uint8(2), res.Admin.ThresholdMed)
		assert.Equal(t, uint8(2), res.Admin.ThresholdHigh)
	})

	t.Run("reports contract admin and its WASM hash", func(t *testing.T) {
		t.Parallel()

		fake := stellartest.NewFake(t)
		setSimulateReturn(t, fake, "admin", "addr:"+stellartest.WasmContract)

		p := newProber(t, fake, health.DefaultThresholds)
		res, err := p.Check(context.Background(), probe.CheckRequest{
			ContractID: stellartest.SACContract,
			AdminFn:    "admin",
		})
		require.NoError(t, err)
		assert.True(t, res.OK)
		assert.Equal(t, probe.OutcomePass, outcomes(res)["admin_posture"])
		require.NotNil(t, res.Admin)
		assert.Equal(t, "contract", res.Admin.Kind)
		assert.Equal(t, stellartest.WasmContract, res.Admin.Address)
		assert.Equal(t, "wasm", res.Admin.Executable)
		assert.Len(t, res.Admin.WasmHash, 64)
	})

	t.Run("resolves admin from configured storage key", func(t *testing.T) {
		t.Parallel()

		fake := stellartest.NewFake(t)
		// Store a contract data entry under sym:Admin holding WasmContract.
		codec := scval.NewRegistry()
		keyVal, err := codec.Encode("sym:Admin")
		require.NoError(t, err)
		addrVal, err := codec.Encode("addr:" + stellartest.WasmContract)
		require.NoError(t, err)
		contract, err := codec.Encode("addr:" + stellartest.SACContract)
		require.NoError(t, err)
		lk := xdr.LedgerKey{
			Type: xdr.LedgerEntryTypeContractData,
			ContractData: &xdr.LedgerKeyContractData{
				Contract:   *contract.Address,
				Key:        keyVal,
				Durability: xdr.ContractDataDurabilityPersistent,
			},
		}
		keyXDR, err := xdr.MarshalBase64(lk)
		require.NoError(t, err)
		dataXDR, err := xdr.MarshalBase64(xdr.LedgerEntryData{
			Type: xdr.LedgerEntryTypeContractData,
			ContractData: &xdr.ContractDataEntry{
				Contract:   *contract.Address,
				Key:        keyVal,
				Durability: xdr.ContractDataDurabilityPersistent,
				Val:        addrVal,
			},
		})
		require.NoError(t, err)
		fake.Entries[keyXDR] = protocol.LedgerEntryResult{KeyXDR: keyXDR, DataXDR: dataXDR}

		p := newProber(t, fake, health.DefaultThresholds)
		res, err := p.Check(context.Background(), probe.CheckRequest{
			ContractID: stellartest.SACContract,
			AdminKey:   "sym:Admin",
		})
		require.NoError(t, err)
		assert.True(t, res.OK)
		assert.Equal(t, probe.OutcomePass, outcomes(res)["admin_posture"])
		require.NotNil(t, res.Admin)
		assert.Equal(t, "key:sym:Admin", res.Admin.Source)
		assert.Equal(t, stellartest.WasmContract, res.Admin.Address)
	})
}
