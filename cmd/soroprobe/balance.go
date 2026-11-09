package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/soroworks/soroprobe/internal/probe"
)

func newBalanceCmd(opts *options) *cobra.Command {
	var file string

	cmd := &cobra.Command{
		Use:   "balance <token-contract-id> [address...] [--file <holders.txt>]",
		Short: "Read SEP-41 or SAC token balances for one or more holders",
		Long: `Simulate decimals(), symbol() and balance(id) against a SEP-41 token or
Stellar Asset Contract and print each holder's formatted token balance.

Both Stellar account addresses (G...) and contract addresses (C...) are accepted.
--file reads holder addresses from a file, one per line (blank lines and # comments ignored).`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 {
				return fmt.Errorf("a token contract ID is required")
			}
			if file == "" && len(args) < 2 {
				return fmt.Errorf("give at least one holder address or --file")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			holders, err := mergeHolders(cmd.InOrStdin(), file, args[1:])
			if err != nil {
				return err
			}

			p, cleanup, err := opts.prober()
			if err != nil {
				return err
			}
			defer cleanup()

			result, err := p.Balances(cmd.Context(), probe.BalanceRequest{
				TokenID: args[0],
				Holders: holders,
			})
			if err != nil {
				return err
			}

			if opts.jsonOut {
				return writeJSON(cmd.OutOrStdout(), result)
			}
			return renderBalance(cmd.OutOrStdout(), result)
		},
	}

	cmd.Flags().StringVarP(&file, "file", "f", "", `read holder addresses from a file, one per line ("-" for stdin)`)
	return cmd
}

func mergeHolders(stdin io.Reader, path string, flagHolders []string) ([]string, error) {
	var holders []string
	if path != "" {
		r := stdin
		if path != "-" {
			f, err := os.Open(path) //nolint:gosec // --file is an operator-supplied path.
			if err != nil {
				return nil, err
			}
			defer func() { _ = f.Close() }()
			r = f
		}
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			holders = append(holders, line)
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("read holders file %s: %w", path, err)
		}
	}
	holders = append(holders, flagHolders...)
	if len(holders) == 0 {
		return nil, fmt.Errorf("no holder addresses found")
	}
	return holders, nil
}

func renderBalance(w io.Writer, r *probe.BalanceResult) error {
	s := styler{on: colorEnabled(w)}
	bw := &bufWriter{w: w}

	header := r.TokenID
	if r.Symbol != "" {
		header = fmt.Sprintf("%s (%s, %d decimals)", r.TokenID, r.Symbol, r.Decimals)
	} else {
		header = fmt.Sprintf("%s (%d decimals)", r.TokenID, r.Decimals)
	}
	bw.printf("%s %s\n\n", s.bold("token"), header)

	for _, b := range r.Balances {
		if b.Error != "" {
			bw.printf("  %s  %s  %s\n", b.Address, s.red("ERROR"), b.Error)
			continue
		}
		amount := b.Formatted
		if r.Symbol != "" {
			amount += " " + r.Symbol
		}
		bw.printf("  %s  %s\n", b.Address, s.bold(amount))
	}
	bw.printf("\n%s\n", s.dim(fmt.Sprintf("checked at ledger %d", r.LatestLedger)))
	return bw.err
}
