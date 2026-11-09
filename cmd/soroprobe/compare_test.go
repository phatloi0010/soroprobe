package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/config"
	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func TestCompareCommand(t *testing.T) {
	t.Run("succeeds when WASMs match on --testnet and --mainnet", func(t *testing.T) {
		t.Chdir(t.TempDir())
		cmd, opts := newRootCmd()
		opts.newProber = func(config.Config) (*probe.Prober, func(), error) {
			fake := stellartest.NewFake(t)
			p, err := probe.New(probe.Options{
				Client:        fake,
				SourceAccount: stellartest.SourceAccount,
				Thresholds:    health.DefaultThresholds,
			})
			return p, func() {}, err
		}

		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{
			"compare",
			"--testnet", stellartest.WasmContract,
			"--mainnet", stellartest.WasmContract,
			"--fn", "decimals",
		})

		require.NoError(t, cmd.Execute())
		assert.Contains(t, out.String(), "MATCH")
	})

	t.Run("exits non-zero when WASMs differ across configured profiles", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		cfgJSON := `{
  "profiles": {
    "staging": {"rpc_url": "http://127.0.0.1:8000", "network_passphrase": "Test SDF Network ; September 2015"},
    "prod": {"rpc_url": "http://127.0.0.1:8001", "network_passphrase": "Public Global Stellar Network ; September 2015"}
  }
}`
		cfgPath := filepath.Join(dir, "soroprobe.json")
		require.NoError(t, os.WriteFile(cfgPath, []byte(cfgJSON), 0o600))

		cmd, opts := newRootCmd()
		opts.newProber = func(config.Config) (*probe.Prober, func(), error) {
			fake := stellartest.NewFake(t)
			p, err := probe.New(probe.Options{
				Client:        fake,
				SourceAccount: stellartest.SourceAccount,
				Thresholds:    health.DefaultThresholds,
			})
			return p, func() {}, err
		}

		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{
			"compare",
			"--config", cfgPath,
			"--left", "staging:" + stellartest.WasmContract,
			"--right", "prod:" + stellartest.SACContract,
		})

		err := cmd.Execute()
		require.Error(t, err)
		var failed *checkFailedError
		assert.ErrorAs(t, err, &failed)
		assert.Contains(t, out.String(), "DIFF")
	})
}
