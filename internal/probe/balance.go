package probe

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/scval"
	"github.com/soroworks/soroprobe/internal/stellar"
)

// BalanceRequest describes a token balance query for one or more holders.
type BalanceRequest struct {
	// TokenID is the SEP-41 or SAC token contract address ("C...").
	TokenID string `json:"token_id"`
	// Holders lists the account ("G...") or contract ("C...") addresses to query.
	Holders []string `json:"holders"`
}

// HolderBalance is the balance of one holder.
type HolderBalance struct {
	Address   string `json:"address"`
	Raw       string `json:"raw,omitempty"`
	Formatted string `json:"formatted,omitempty"`
	Error     string `json:"error,omitempty"`
}

// BalanceResult aggregates token metadata and holder balances.
type BalanceResult struct {
	TokenID      string          `json:"token_id"`
	Symbol       string          `json:"symbol"`
	Decimals     uint32          `json:"decimals"`
	Balances     []HolderBalance `json:"balances"`
	LatestLedger uint32          `json:"latest_ledger"`
}

// Balances queries a token's decimals and symbol, then simulates balance(id)
// for every holder in req.Holders.
func (p *Prober) Balances(ctx context.Context, req BalanceRequest) (*BalanceResult, error) {
	if _, err := stellar.ParseContractID(req.TokenID); err != nil {
		return nil, err
	}
	if len(req.Holders) == 0 {
		return nil, errors.New("at least one holder address is required")
	}

	normalized := make([]string, len(req.Holders))
	for i, h := range req.Holders {
		addr, err := normalizeHolderAddress(h)
		if err != nil {
			return nil, err
		}
		normalized[i] = addr
	}

	decSim, err := p.Simulate(ctx, SimulateRequest{
		ContractID: req.TokenID,
		Function:   "decimals",
	})
	if err != nil {
		return nil, err
	}
	if !decSim.Success {
		return nil, fmt.Errorf("querying decimals() on %s failed: %s", req.TokenID, decSim.Error)
	}
	decimals, err := parseDecimals(decSim)
	if err != nil {
		return nil, err
	}

	var symbol string
	symSim, err := p.Simulate(ctx, SimulateRequest{
		ContractID: req.TokenID,
		Function:   "symbol",
	})
	if err != nil {
		return nil, err
	}
	if symSim.Success {
		if s, ok := symSim.ReturnValue.(string); ok {
			symbol = s
		}
	}

	result := &BalanceResult{
		TokenID:      req.TokenID,
		Symbol:       symbol,
		Decimals:     decimals,
		Balances:     make([]HolderBalance, 0, len(normalized)),
		LatestLedger: decSim.LatestLedger,
	}

	for _, holder := range normalized {
		balSim, err := p.Simulate(ctx, SimulateRequest{
			ContractID: req.TokenID,
			Function:   "balance",
			Args:       []string{"addr:" + holder},
		})
		if err != nil {
			return nil, err
		}
		if balSim.LatestLedger > result.LatestLedger {
			result.LatestLedger = balSim.LatestLedger
		}
		if !balSim.Success {
			result.Balances = append(result.Balances, HolderBalance{
				Address: holder,
				Error:   balSim.Error,
			})
			continue
		}

		rawInt, err := extractBalanceBigInt(balSim)
		if err != nil {
			result.Balances = append(result.Balances, HolderBalance{
				Address: holder,
				Error:   err.Error(),
			})
			continue
		}

		result.Balances = append(result.Balances, HolderBalance{
			Address:   holder,
			Raw:       rawInt.String(),
			Formatted: FormatTokenAmount(rawInt, decimals),
		})
	}

	return result, nil
}

func normalizeHolderAddress(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	for _, prefix := range []string{"address:", "addr:"} {
		if strings.HasPrefix(strings.ToLower(s), prefix) {
			s = strings.TrimSpace(s[len(prefix):])
			break
		}
	}
	if strkey.IsValidEd25519PublicKey(s) || strkey.IsValidContractAddress(s) {
		return s, nil
	}
	return "", fmt.Errorf("invalid holder address %q: expected a G... account or C... contract address", raw)
}

func parseDecimals(sim *SimulateResult) (uint32, error) {
	if u, ok := sim.ReturnValue.(uint32); ok {
		return u, nil
	}
	if sim.ReturnValueXDR != "" {
		var val xdr.ScVal
		if err := xdr.SafeUnmarshalBase64(sim.ReturnValueXDR, &val); err == nil {
			if n, ok := scval.ToBigInt(val); ok && n.Sign() >= 0 && n.IsUint64() && n.Uint64() <= 255 {
				return uint32(n.Uint64()), nil
			}
		}
	}
	return 0, fmt.Errorf("unexpected decimals() return value %v", sim.ReturnValue)
}

func extractBalanceBigInt(sim *SimulateResult) (*big.Int, error) {
	if sim.ReturnValueXDR != "" {
		var val xdr.ScVal
		if err := xdr.SafeUnmarshalBase64(sim.ReturnValueXDR, &val); err != nil {
			return nil, fmt.Errorf("decode balance return value: %w", err)
		}
		if n, ok := scval.ToBigInt(val); ok {
			return n, nil
		}
	}
	if s, ok := sim.ReturnValue.(string); ok {
		if n, ok := new(big.Int).SetString(s, 10); ok {
			return n, nil
		}
	}
	return nil, fmt.Errorf("unexpected balance() return value %v", sim.ReturnValue)
}

// FormatTokenAmount formats an integer token amount with the given decimal places.
func FormatTokenAmount(amount *big.Int, decimals uint32) string {
	if amount == nil {
		return "0"
	}
	if decimals == 0 {
		return amount.String()
	}

	neg := amount.Sign() < 0
	abs := new(big.Int).Abs(amount)
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	whole, frac := new(big.Int).QuoRem(abs, scale, new(big.Int))

	fracStr := fmt.Sprintf("%0*s", int(decimals), frac.String())
	fracStr = strings.TrimRight(fracStr, "0")
	if fracStr == "" {
		fracStr = "0"
	}

	prefix := ""
	if neg {
		prefix = "-"
	}
	return fmt.Sprintf("%s%s.%s", prefix, whole.String(), fracStr)
}
