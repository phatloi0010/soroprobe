package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/config"
	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/scval"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func newBalanceFakeProber(t *testing.T) proberFactory {
	t.Helper()
	return func(config.Config) (*probe.Prober, func(), error) {
		fake := stellartest.NewFake(t)
		codec := scval.NewRegistry()

		symVal, err := codec.Encode(`str:"XLM"`)
		require.NoError(t, err)
		symXDR, err := xdr.MarshalBase64(symVal)
		require.NoError(t, err)
		symResp := stellartest.LoadSimulate(t, "simulate_success.json")
		symResp.Results[0].ReturnValueXDR = &symXDR
		fake.Simulations["symbol"] = symResp

		balVal, err := codec.Encode("i128:250000000")
		require.NoError(t, err)
		balXDR, err := xdr.MarshalBase64(balVal)
		require.NoError(t, err)
		balResp := stellartest.LoadSimulate(t, "simulate_success.json")
		balResp.Results[0].ReturnValueXDR = &balXDR
		fake.Simulations["balance"] = balResp

		p, err := probe.New(probe.Options{
			Client:        fake,
			SourceAccount: stellartest.SourceAccount,
			Thresholds:    health.DefaultThresholds,
		})
		return p, func() {}, err
	}
}

func TestBalanceCommand(t *testing.T) {
	t.Run("text output for multiple holders", func(t *testing.T) {
		t.Chdir(t.TempDir())
		cmd, opts := newRootCmd()
		opts.newProber = newBalanceFakeProber(t)

		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{
			"balance",
			"--source-account", stellartest.SourceAccount,
			stellartest.SACContract,
			stellartest.SourceAccount,
			stellartest.WasmContract,
		})

		require.NoError(t, cmd.Execute())
		text := out.String()
		assert.Contains(t, text, "25.0 XLM")
		assert.Contains(t, text, stellartest.SourceAccount)
		assert.Contains(t, text, stellartest.WasmContract)
	})

	t.Run("reads holders from --file and emits --json", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		holdersPath := filepath.Join(dir, "holders.txt")
		content := "# comment\n\n" + stellartest.SourceAccount + "\n" + stellartest.WasmContract + "\n"
		require.NoError(t, os.WriteFile(holdersPath, []byte(content), 0o600))

		cmd, opts := newRootCmd()
		opts.newProber = newBalanceFakeProber(t)

		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{
			"balance",
			"--source-account", stellartest.SourceAccount,
			"--file", holdersPath,
			"--json",
			stellartest.SACContract,
		})

		require.NoError(t, cmd.Execute())
		var res probe.BalanceResult
		require.NoError(t, json.Unmarshal(out.Bytes(), &res))
		assert.Equal(t, "XLM", res.Symbol)
		assert.Equal(t, uint32(7), res.Decimals)
		require.Len(t, res.Balances, 2)
		assert.Equal(t, "25.0", res.Balances[0].Formatted)
	})
}
