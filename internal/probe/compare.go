package probe

import (
	"context"
	"errors"
	"fmt"

	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/health"
)

// CompareTarget identifies one side of a cross-network comparison.
type CompareTarget struct {
	Name       string  `json:"name"`
	ContractID string  `json:"contract_id"`
	Prober     *Prober `json:"-"`
}

// CompareCall describes a read-only call to simulate on both networks.
type CompareCall struct {
	Function string   `json:"function"`
	Args     []string `json:"args,omitempty"`
}

// CompareRequest configures a cross-network contract comparison.
type CompareRequest struct {
	Left           CompareTarget     `json:"left"`
	Right          CompareTarget     `json:"right"`
	DataKeys       []string          `json:"data_keys,omitempty"`
	DataDurability health.Durability `json:"data_durability,omitempty"`
	Calls          []CompareCall     `json:"calls,omitempty"`
}

// EntryComparison compares a single state entry across both networks.
type EntryComparison struct {
	Kind         string        `json:"kind"`
	Key          string        `json:"key,omitempty"`
	LeftStatus   health.Status `json:"left_status"`
	RightStatus  health.Status `json:"right_status"`
	LeftLedgers  int64         `json:"left_ledgers_remaining"`
	RightLedgers int64         `json:"right_ledgers_remaining"`
	StatusMatch  bool          `json:"status_match"`
}

// CallComparison compares a read-only simulation across both networks.
type CallComparison struct {
	Function string          `json:"function"`
	Args     []string        `json:"args,omitempty"`
	Left     *SimulateResult `json:"left"`
	Right    *SimulateResult `json:"right"`
	Match    bool            `json:"match"`
}

// CompareResult holds the cross-network comparison report.
type CompareResult struct {
	LeftName       string            `json:"left_name"`
	RightName      string            `json:"right_name"`
	LeftContract   string            `json:"left_contract_id"`
	RightContract  string            `json:"right_contract_id"`
	LeftInspect    *InspectResult    `json:"left_inspect"`
	RightInspect   *InspectResult    `json:"right_inspect"`
	WasmMatch      bool              `json:"wasm_match"`
	ExecutableDiff bool              `json:"executable_diff"`
	Entries        []EntryComparison `json:"entries"`
	Calls          []CallComparison  `json:"calls,omitempty"`
	Differences    []string          `json:"differences,omitempty"`
}

// Compare inspects and simulates the target contracts on both networks and
// reports WASM equality, per-entry TTL health, and read-only call results.
func Compare(ctx context.Context, req CompareRequest) (*CompareResult, error) {
	if req.Left.Prober == nil || req.Right.Prober == nil {
		return nil, errors.New("compare: both network probers are required")
	}
	if req.Left.ContractID == "" || req.Right.ContractID == "" {
		return nil, errors.New("compare: both contract IDs are required")
	}

	leftName := req.Left.Name
	if leftName == "" {
		leftName = "left"
	}
	rightName := req.Right.Name
	if rightName == "" {
		rightName = "right"
	}

	leftInspect, err := req.Left.Prober.Inspect(ctx, InspectRequest{
		ContractID:     req.Left.ContractID,
		DataKeys:       req.DataKeys,
		DataDurability: req.DataDurability,
	})
	if err != nil {
		return nil, fmt.Errorf("%s inspect: %w", leftName, err)
	}

	rightInspect, err := req.Right.Prober.Inspect(ctx, InspectRequest{
		ContractID:     req.Right.ContractID,
		DataKeys:       req.DataKeys,
		DataDurability: req.DataDurability,
	})
	if err != nil {
		return nil, fmt.Errorf("%s inspect: %w", rightName, err)
	}

	res := &CompareResult{
		LeftName:      leftName,
		RightName:     rightName,
		LeftContract:  req.Left.ContractID,
		RightContract: req.Right.ContractID,
		LeftInspect:   leftInspect,
		RightInspect:  rightInspect,
	}

	res.WasmMatch = leftInspect.Deployed &&
		rightInspect.Deployed &&
		leftInspect.Executable == rightInspect.Executable &&
		leftInspect.WasmHash == rightInspect.WasmHash
	res.ExecutableDiff = leftInspect.Executable != rightInspect.Executable

	if !leftInspect.Deployed || !rightInspect.Deployed {
		res.Differences = append(res.Differences, fmt.Sprintf(
			"deployment mismatch: %s deployed=%t, %s deployed=%t",
			leftName, leftInspect.Deployed, rightName, rightInspect.Deployed,
		))
	} else if !res.WasmMatch {
		res.Differences = append(res.Differences, fmt.Sprintf(
			"WASM mismatch: %s (%s %s) != %s (%s %s)",
			leftName, leftInspect.Executable, leftInspect.WasmHash,
			rightName, rightInspect.Executable, rightInspect.WasmHash,
		))
	}

	addEntryCmp := func(kind, key string, l, r EntryReport) {
		cmp := EntryComparison{
			Kind:         kind,
			Key:          key,
			LeftStatus:   l.Status,
			RightStatus:  r.Status,
			LeftLedgers:  l.LedgersRemaining,
			RightLedgers: r.LedgersRemaining,
			StatusMatch:  l.Status == r.Status,
		}
		res.Entries = append(res.Entries, cmp)
		if !cmp.StatusMatch {
			label := kind
			if key != "" {
				label += ":" + key
			}
			res.Differences = append(res.Differences, fmt.Sprintf(
				"TTL status difference on %s: %s=%s (%d ledgers) vs %s=%s (%d ledgers)",
				label, leftName, l.Status, l.LedgersRemaining, rightName, r.Status, r.LedgersRemaining,
			))
		}
	}

	addEntryCmp("instance", "", leftInspect.Instance, rightInspect.Instance)
	if leftInspect.Code != nil || rightInspect.Code != nil {
		var lCode, rCode EntryReport
		if leftInspect.Code != nil {
			lCode = *leftInspect.Code
		} else {
			lCode = EntryReport{Kind: "code", TTL: health.TTL{Status: health.StatusMissing}}
		}
		if rightInspect.Code != nil {
			rCode = *rightInspect.Code
		} else {
			rCode = EntryReport{Kind: "code", TTL: health.TTL{Status: health.StatusMissing}}
		}
		addEntryCmp("code", "", lCode, rCode)
	}
	for i := 0; i < len(leftInspect.Data) && i < len(rightInspect.Data); i++ {
		addEntryCmp("data", leftInspect.Data[i].Key, leftInspect.Data[i], rightInspect.Data[i])
	}

	if leftInspect.Deployed && rightInspect.Deployed {
		for _, call := range req.Calls {
			lSim, err := req.Left.Prober.Simulate(ctx, SimulateRequest{
				ContractID: req.Left.ContractID,
				Function:   call.Function,
				Args:       call.Args,
			})
			if err != nil {
				return nil, fmt.Errorf("%s simulate %s: %w", leftName, call.Function, err)
			}
			rSim, err := req.Right.Prober.Simulate(ctx, SimulateRequest{
				ContractID: req.Right.ContractID,
				Function:   call.Function,
				Args:       call.Args,
			})
			if err != nil {
				return nil, fmt.Errorf("%s simulate %s: %w", rightName, call.Function, err)
			}

			match := compareSimulations(lSim, rSim)
			res.Calls = append(res.Calls, CallComparison{
				Function: call.Function,
				Args:     call.Args,
				Left:     lSim,
				Right:    rSim,
				Match:    match,
			})
			if !match {
				res.Differences = append(res.Differences, fmt.Sprintf(
					"call %s result difference: %s=%s vs %s=%s",
					call.Function, leftName, summarizeSim(lSim), rightName, summarizeSim(rSim),
				))
			}
		}
	}

	return res, nil
}

func compareSimulations(left, right *SimulateResult) bool {
	if left.Success != right.Success {
		return false
	}
	if !left.Success {
		return left.Error == right.Error
	}
	var lVal, rVal xdr.ScVal
	if left.ReturnValueXDR != "" {
		if err := xdr.SafeUnmarshalBase64(left.ReturnValueXDR, &lVal); err != nil {
			return false
		}
	} else {
		lVal = xdr.ScVal{Type: xdr.ScValTypeScvVoid}
	}
	if right.ReturnValueXDR != "" {
		if err := xdr.SafeUnmarshalBase64(right.ReturnValueXDR, &rVal); err != nil {
			return false
		}
	} else {
		rVal = xdr.ScVal{Type: xdr.ScValTypeScvVoid}
	}
	match, _ := compareScVals(lVal, rVal)
	return match
}

func summarizeSim(s *SimulateResult) string {
	if !s.Success {
		return "error(" + s.Error + ")"
	}
	return formatVal(s.ReturnValue)
}
