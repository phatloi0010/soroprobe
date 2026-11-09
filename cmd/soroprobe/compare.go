package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stellar/go-stellar-sdk/network"

	"github.com/soroworks/soroprobe/internal/config"
	"github.com/soroworks/soroprobe/internal/probe"
)

func newCompareCmd(opts *options) *cobra.Command {
	var testnetContract string
	var mainnetContract string
	var leftSpec string
	var rightSpec string
	var function string
	var callArgs []string
	var dataKeys []string
	var durability string

	cmd := &cobra.Command{
		Use:   "compare --testnet <contract-id> --mainnet <contract-id> | compare --left <profile>:<contract-id> --right <profile>:<contract-id>",
		Short: "Compare a contract deployment across two networks",
		Long: `Compare a contract across two networks (such as testnet and mainnet, or any
two configured profiles): verify WASM hash equality, compare per-entry TTL
health, and compare the results of read-only calls on both.

Exits 0 when both contracts are deployed and run the same WASM executable,
and 1 when their WASM hashes differ or one side is not deployed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			leftProfile, leftContract, rightProfile, rightContract, err := resolveCompareSides(
				testnetContract, mainnetContract, leftSpec, rightSpec,
			)
			if err != nil {
				return err
			}

			d, err := parseDurability(durability)
			if err != nil {
				return err
			}

			leftProber, leftCleanup, err := opts.proberForNetwork(leftProfile)
			if err != nil {
				return err
			}
			defer leftCleanup()

			rightProber, rightCleanup, err := opts.proberForNetwork(rightProfile)
			if err != nil {
				return err
			}
			defer rightCleanup()

			var calls []probe.CompareCall
			if function != "" {
				calls = append(calls, probe.CompareCall{
					Function: function,
					Args:     callArgs,
				})
			}

			result, err := probe.Compare(cmd.Context(), probe.CompareRequest{
				Left: probe.CompareTarget{
					Name:       leftProfile,
					ContractID: leftContract,
					Prober:     leftProber,
				},
				Right: probe.CompareTarget{
					Name:       rightProfile,
					ContractID: rightContract,
					Prober:     rightProber,
				},
				DataKeys:       dataKeys,
				DataDurability: d,
				Calls:          calls,
			})
			if err != nil {
				return err
			}

			if opts.jsonOut {
				err = writeJSON(cmd.OutOrStdout(), result)
			} else {
				err = renderCompare(cmd.OutOrStdout(), result)
			}
			if err != nil {
				return err
			}

			if !result.WasmMatch {
				return &checkFailedError{msg: fmt.Sprintf("WASM mismatch between %s and %s", leftProfile, rightProfile)}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&testnetContract, "testnet", "", "contract ID on the testnet profile/network")
	cmd.Flags().StringVar(&mainnetContract, "mainnet", "", "contract ID on the mainnet profile/network")
	cmd.Flags().StringVar(&leftSpec, "left", "", "left network and contract in <profile>:<contract-id> form")
	cmd.Flags().StringVar(&rightSpec, "right", "", "right network and contract in <profile>:<contract-id> form")
	cmd.Flags().StringVar(&function, "fn", "", "read-only function to simulate and compare on both networks")
	cmd.Flags().StringArrayVar(&callArgs, "arg", nil, "argument for --fn, in type:value form (repeatable)")
	cmd.Flags().StringArrayVar(&dataKeys, "key", nil, "contract data key to compare across both networks (repeatable)")
	cmd.Flags().StringVar(&durability, "durability", "persistent", "durability for --key lookups: persistent or temporary")
	return cmd
}

func resolveCompareSides(testnet, mainnet, left, right string) (string, string, string, string, error) {
	if testnet != "" || mainnet != "" {
		if left != "" || right != "" {
			return "", "", "", "", fmt.Errorf("use either --testnet/--mainnet or --left/--right, not both")
		}
		if testnet == "" || mainnet == "" {
			return "", "", "", "", fmt.Errorf("both --testnet and --mainnet contract IDs are required")
		}
		return "testnet", testnet, "mainnet", mainnet, nil
	}
	if left == "" || right == "" {
		return "", "", "", "", fmt.Errorf("specify --testnet and --mainnet, or --left and --right")
	}
	lp, lc, err := splitProfileContract(left)
	if err != nil {
		return "", "", "", "", err
	}
	rp, rc, err := splitProfileContract(right)
	if err != nil {
		return "", "", "", "", err
	}
	return lp, lc, rp, rc, nil
}

func splitProfileContract(spec string) (string, string, error) {
	idx := strings.IndexByte(spec, ':')
	if idx <= 0 || idx == len(spec)-1 {
		return "", "", fmt.Errorf("expected <profile>:<contract-id>, got %q", spec)
	}
	return strings.TrimSpace(spec[:idx]), strings.TrimSpace(spec[idx+1:]), nil
}

func (o *options) proberForNetwork(profileName string) (*probe.Prober, func(), error) {
	cfg, err := config.LoadWithProfile(o.configPath, profileName)
	if err != nil {
		// Built-in fallback for standard network names when no config file defines them.
		switch profileName {
		case "testnet":
			cfg = o.cfg
			cfg.NetworkPassphrase = network.TestNetworkPassphrase
		case "mainnet", "public":
			cfg = o.cfg
			cfg.NetworkPassphrase = network.PublicNetworkPassphrase
		default:
			return nil, nil, err
		}
	}
	if o.newProber != nil {
		return o.newProber(cfg)
	}
	return newLiveProber(cfg, o.abiFromChain)
}

func renderCompare(w io.Writer, r *probe.CompareResult) error {
	s := styler{on: colorEnabled(w)}
	bw := &bufWriter{w: w}

	bw.printf("%s %s (%s) vs %s (%s)\n\n",
		s.bold("compare"), r.LeftName, r.LeftContract, r.RightName, r.RightContract)

	if r.WasmMatch {
		bw.printf("  %s  %-18s %s (%s)\n", s.green("MATCH"), "wasm", r.LeftInspect.Executable, r.LeftInspect.WasmHash)
	} else {
		bw.printf("  %s  %-18s %s=%s (%s) vs %s=%s (%s)\n",
			s.red("DIFF "), "wasm",
			r.LeftName, r.LeftInspect.Executable, r.LeftInspect.WasmHash,
			r.RightName, r.RightInspect.Executable, r.RightInspect.WasmHash)
	}

	bw.printf("\n%s\n", s.bold("entries"))
	for _, e := range r.Entries {
		label := e.Kind
		if e.Key != "" {
			label += ":" + e.Key
		}
		tag := s.green("MATCH")
		if !e.StatusMatch {
			tag = s.wrap(ansiYellow, "DIFF ")
		}
		bw.printf("  %s  %-18s %s=%s (%d ledgers) | %s=%s (%d ledgers)\n",
			tag, label,
			r.LeftName, e.LeftStatus, e.LeftLedgers,
			r.RightName, e.RightStatus, e.RightLedgers)
	}

	if len(r.Calls) > 0 {
		bw.printf("\n%s\n", s.bold("calls"))
		for _, c := range r.Calls {
			tag := s.green("MATCH")
			if !c.Match {
				tag = s.red("DIFF ")
			}
			bw.printf("  %s  %-18s %s=%v | %s=%v\n",
				tag, c.Function,
				r.LeftName, c.Left.ReturnValue,
				r.RightName, c.Right.ReturnValue)
		}
	}

	if len(r.Differences) > 0 {
		bw.printf("\n%s\n", s.bold("differences"))
		for _, diff := range r.Differences {
			bw.printf("  - %s\n", s.wrap(ansiYellow, diff))
		}
	}

	return bw.err
}
