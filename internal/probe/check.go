package probe

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/scval"
	"github.com/soroworks/soroprobe/internal/stellar"
)

// baseReserveStroops is the Stellar network base reserve (0.5 XLM = 5,000,000 stroops).
const baseReserveStroops int64 = 5_000_000

// CheckRequest describes a combined health check.
type CheckRequest struct {
	// ContractID is the strkey contract address ("C...").
	ContractID string `json:"contract_id" yaml:"contract_id"`
	// Function, when set, names a read-only function to simulate as a
	// liveness probe.
	//
	// It is optional on purpose. SoroProbe cannot discover a contract's
	// exported functions from the ledger, and inventing a name would produce a
	// misleading failure, so the simulation step is reported as skipped rather
	// than guessed at.
	Function string `json:"function,omitempty" yaml:"function,omitempty"`
	// Args are argument specifications for Function.
	Args []string `json:"args,omitempty" yaml:"args,omitempty"`
	// Expect is an optional argument specification for the expected return value.
	Expect string `json:"expect,omitempty" yaml:"expect,omitempty"`
	// DataKeys are optional data entries to include in the TTL assessment.
	DataKeys []string `json:"data_keys,omitempty" yaml:"data_keys,omitempty"`
	// DataDurability selects the durability DataKeys are read at.
	DataDurability health.Durability `json:"data_durability,omitempty" yaml:"data_durability,omitempty"`
	// RentPayer, when set, names a Stellar account ("G...") whose spendable
	// XLM balance is compared against the estimated rent for expiring entries.
	RentPayer string `json:"rent_payer,omitempty" yaml:"rent_payer,omitempty"`
}

// RentPayerReport shows the spendable-balance vs. extension-fee calculation.
type RentPayerReport struct {
	AccountID          string `json:"account_id"`
	Exists             bool   `json:"exists"`
	BalanceStroops     int64  `json:"balance_stroops"`
	ReserveStroops     int64  `json:"reserve_stroops"`
	SpendableStroops   int64  `json:"spendable_stroops"`
	ExpiringEntries    int    `json:"expiring_entries"`
	EstimatedRent      int64  `json:"estimated_rent_stroops"`
	NumSubEntries      uint32 `json:"num_sub_entries"`
	NumSponsoring      uint32 `json:"num_sponsoring,omitempty"`
	NumSponsored       uint32 `json:"num_sponsored,omitempty"`
	SellingLiabilities int64  `json:"selling_liabilities_stroops,omitempty"`
}

// CheckOutcome is the result of one individual check.
type CheckOutcome string

const (
	// OutcomePass means the check succeeded.
	OutcomePass CheckOutcome = "pass"
	// OutcomeWarn means the check succeeded but something needs attention.
	OutcomeWarn CheckOutcome = "warn"
	// OutcomeFail means the check failed.
	OutcomeFail CheckOutcome = "fail"
	// OutcomeSkip means the check was not run.
	OutcomeSkip CheckOutcome = "skip"
)

// Check is one named assertion about a contract.
type Check struct {
	Name    string       `json:"name"`
	Outcome CheckOutcome `json:"outcome"`
	Detail  string       `json:"detail"`
}

// CheckResult aggregates the individual checks.
type CheckResult struct {
	ContractID string `json:"contract_id"`
	// OK is false when any check failed. The CLI exits non-zero on false.
	OK bool `json:"ok"`
	// Status is the worst state-health status observed.
	Status health.Status `json:"status"`
	// Checks lists each assertion in the order it was evaluated.
	Checks []Check `json:"checks"`
	// Inspect is the full state-health report the checks were derived from.
	Inspect *InspectResult `json:"inspect,omitempty"`
	// Simulate is the invocation result, when a function was given.
	Simulate *SimulateResult `json:"simulate,omitempty"`
	// RentPayer is the rent-payer affordability calculation, when requested.
	RentPayer *RentPayerReport `json:"rent_payer,omitempty"`
	// LatestLedger is the ledger the assessment was made against.
	LatestLedger uint32 `json:"latest_ledger"`
	// CloseTimeNote explains the measured duration estimate or its fallback.
	CloseTimeNote string `json:"close_time_note"`
}

// CheckStep is one pluggable step in the health check pipeline.
//
// Run receives the request and the state gathered once by Check — the inspect
// report and, when one was needed, the simulation — and returns the checks it
// contributes, in the order they should be reported. A step that has nothing to
// say returns no checks; a step that does not apply usually returns a single
// OutcomeSkip check so the report still names it.
//
// Returning errStop ends the pipeline after the step's checks are recorded. Any
// other error aborts Check.
type CheckStep interface {
	Name() string
	Run(ctx context.Context, req CheckRequest, inspect *InspectResult, simulate *SimulateResult) ([]Check, error)
}

// errStop ends the pipeline once a step's checks are recorded. Only the
// deployment step uses it: with no instance there is nothing further worth
// probing, which is the one early stop the checks have always had. Other
// failures are still reported in full.
var errStop = errors.New("stop checking")

// checkSteps is the ordered list Check runs. To add a check, implement
// CheckStep and append it here at the position its result should appear; a step
// reads the inspect and simulation state Check gathered once, so it cannot add
// an RPC call by itself. It is built per Prober because some steps, such as the
// return-value assertion, need the prober's argument codec.
func (p *Prober) checkSteps() []CheckStep {
	return []CheckStep{
		&deployedStep{},
		&instanceTTLStep{},
		&codeTTLStep{},
		&dataTTLStep{},
		&simulateStep{},
		&expectStep{p: p},
	}
}

// expectStep asserts the simulated call's return value when the request names
// an expected value. Without one it contributes nothing.
type expectStep struct{ p *Prober }

func (s *expectStep) Name() string { return "expect" }

func (s *expectStep) Run(_ context.Context, req CheckRequest, _ *InspectResult, simulate *SimulateResult) ([]Check, error) {
	if req.Expect == "" {
		return nil, nil
	}
	return []Check{s.p.expectCheck(req, &CheckResult{Simulate: simulate})}, nil
}

// Check runs the combined health check: is the contract deployed, are its
// instance, code and named data entries live and not near expiration, and — if
// a function was given — does a read-only call simulate successfully.
//
// The work is done by the ordered steps in checkSteps. Check gathers the
// inspect report and the simulation once and hands both to every step, so
// adding a step cannot accidentally add an RPC call.
func (p *Prober) Check(ctx context.Context, req CheckRequest) (*CheckResult, error) {
	inspect, err := p.Inspect(ctx, InspectRequest{
		ContractID:     req.ContractID,
		DataKeys:       req.DataKeys,
		DataDurability: req.DataDurability,
	})
	if err != nil {
		return nil, err
	}

	// Gather the simulation once, and only when a step can use it: an
	// undeployed contract fails the first step, and a check without a function
	// never reports a simulation, so in both cases calling Simulate would spend
	// an RPC call whose result no step reads.
	var sim *SimulateResult
	if inspect.Deployed && req.Function != "" {
		sim, err = p.Simulate(ctx, SimulateRequest{
			ContractID: req.ContractID,
			Function:   req.Function,
			Args:       req.Args,
		})
		if err != nil {
			return nil, err
		}
	}

	result := &CheckResult{
		ContractID:    req.ContractID,
		Inspect:       inspect,
		Status:        inspect.Status,
		Simulate:      sim,
		LatestLedger:  inspect.LatestLedger,
		CloseTimeNote: inspect.CloseTimeNote,
	}

	for _, step := range p.checkSteps() {
		checks, err := step.Run(ctx, req, inspect, sim)
		result.Checks = append(result.Checks, checks...)
		if err != nil {
			if errors.Is(err, errStop) {
				break
			}
			return nil, err
		}
	}

	if inspect.Deployed && req.RentPayer != "" {
		rpCheck, rpReport, err := p.evaluateRentPayer(ctx, req, inspect)
		if err != nil {
			return nil, err
		}
		result.RentPayer = rpReport
		result.Checks = append(result.Checks, rpCheck)
	}

	result.OK = true
	for _, c := range result.Checks {
		if c.Outcome == OutcomeFail {
			result.OK = false
			break
		}
	}
	return result, nil
}

// ttlCheck converts a TTL assessment into a pass/warn/fail outcome. A warning
// threshold breach is deliberately not a failure: it is a signal to extend the
// entry's TTL, not a reason to fail a CI pipeline.
func ttlCheck(name string, entry EntryReport) Check {
	outcome := OutcomeFail
	switch entry.Status {
	case health.StatusHealthy, health.StatusUnknown:
		outcome = OutcomePass
	case health.StatusWarning:
		outcome = OutcomeWarn
	}
	return Check{Name: name, Outcome: outcome, Detail: entry.Summary}
}

// --- step implementations ---

// deployedStep reports whether the contract has an instance on this network.
// It is the pipeline's precondition, so a missing instance stops the run.
type deployedStep struct{}

func (s *deployedStep) Name() string { return "deployed" }

func (s *deployedStep) Run(_ context.Context, _ CheckRequest, inspect *InspectResult, _ *SimulateResult) ([]Check, error) {
	if !inspect.Deployed {
		return []Check{{
			Name:    "deployed",
			Outcome: OutcomeFail,
			Detail:  "no contract instance entry found on this network",
		}}, errStop
	}
	return []Check{{
		Name:    "deployed",
		Outcome: OutcomePass,
		Detail:  fmt.Sprintf("instance found, executable is %s", inspect.Executable),
	}}, nil
}

// instanceTTLStep reports the contract instance's expiration health.
type instanceTTLStep struct{}

func (s *instanceTTLStep) Name() string { return "instance_ttl" }

func (s *instanceTTLStep) Run(_ context.Context, _ CheckRequest, inspect *InspectResult, _ *SimulateResult) ([]Check, error) {
	return []Check{ttlCheck("instance_ttl", inspect.Instance)}, nil
}

// codeTTLStep reports the Wasm code entry's expiration health. Stellar Asset
// Contracts have no code entry, so the step is reported as skipped for them.
type codeTTLStep struct{}

func (s *codeTTLStep) Name() string { return "code_ttl" }

func (s *codeTTLStep) Run(_ context.Context, _ CheckRequest, inspect *InspectResult, _ *SimulateResult) ([]Check, error) {
	if inspect.Code != nil {
		return []Check{ttlCheck("code_ttl", *inspect.Code)}, nil
	}
	return []Check{{
		Name:    "code_ttl",
		Outcome: OutcomeSkip,
		Detail:  "stellar asset contracts are built into the host and have no code entry",
	}}, nil
}

// dataTTLStep reports one check per named data entry. With no keys it
// contributes nothing, matching the report's previous shape.
type dataTTLStep struct{}

func (s *dataTTLStep) Name() string { return "data_ttl" }

func (s *dataTTLStep) Run(_ context.Context, _ CheckRequest, inspect *InspectResult, _ *SimulateResult) ([]Check, error) {
	checks := make([]Check, 0, len(inspect.Data))
	for _, entry := range inspect.Data {
		checks = append(checks, ttlCheck("data_ttl:"+entry.Key, entry))
	}
	return checks, nil
}

// simulateStep reports the read-only call's outcome. Without a function there
// is nothing to assert, so it is reported as skipped.
type simulateStep struct{}

func (s *simulateStep) Name() string { return "simulate" }

func (s *simulateStep) Run(_ context.Context, req CheckRequest, _ *InspectResult, simulate *SimulateResult) ([]Check, error) {
	if req.Function == "" {
		return []Check{{
			Name:    "simulate",
			Outcome: OutcomeSkip,
			Detail:  "no function given; pass one to probe a read-only call",
		}}, nil
	}
	if simulate == nil {
		return nil, errors.New("simulate step has no simulation result")
	}
	if !simulate.Success {
		return []Check{{
			Name:    "simulate",
			Outcome: OutcomeFail,
			Detail:  fmt.Sprintf("calling %s failed: %s", req.Function, simulate.Error),
		}}, nil
	}
	return []Check{{
		Name:    "simulate",
		Outcome: OutcomePass,
		Detail: fmt.Sprintf("calling %s succeeded, %d instructions, min resource fee %d stroops",
			req.Function, simulate.Cost.Instructions, simulate.Cost.MinResourceFee),
	}}, nil
}

func (p *Prober) expectCheck(req CheckRequest, result *CheckResult) Check {
	if req.Function == "" {
		return Check{
			Name:    "expect",
			Outcome: OutcomeFail,
			Detail:  "an expected value was given but no function was specified",
		}
	}
	if result.Simulate == nil || !result.Simulate.Success {
		return Check{
			Name:    "expect",
			Outcome: OutcomeFail,
			Detail:  "cannot check return value because simulation failed or did not run",
		}
	}

	expectedVal, err := p.codec.Encode(req.Expect)
	if err != nil {
		return Check{
			Name:    "expect",
			Outcome: OutcomeFail,
			Detail:  fmt.Sprintf("invalid expected value %q: %v", req.Expect, err),
		}
	}

	var actualVal xdr.ScVal
	if result.Simulate.ReturnValueXDR != "" {
		if err := xdr.SafeUnmarshalBase64(result.Simulate.ReturnValueXDR, &actualVal); err != nil {
			return Check{
				Name:    "expect",
				Outcome: OutcomeFail,
				Detail:  fmt.Sprintf("decode actual return value: %v", err),
			}
		}
	} else {
		actualVal = xdr.ScVal{Type: xdr.ScValTypeScvVoid}
	}

	match, typeMismatch := compareScVals(expectedVal, actualVal)

	expDecoded, _ := p.codec.Decode(expectedVal)
	actDecoded := result.Simulate.ReturnValue
	expDisp := formatVal(expDecoded)
	actDisp := formatVal(actDecoded)

	if match {
		return Check{
			Name:    "expect",
			Outcome: OutcomePass,
			Detail:  fmt.Sprintf("expected %s, got %s", expDisp, actDisp),
		}
	}

	if typeMismatch {
		return Check{
			Name:    "expect",
			Outcome: OutcomeFail,
			Detail:  fmt.Sprintf("type mismatch: expected %s, got %s", expDisp, actDisp),
		}
	}

	return Check{
		Name:    "expect",
		Outcome: OutcomeFail,
		Detail:  fmt.Sprintf("expected %s, got %s", expDisp, actDisp),
	}
}

func compareScVals(expected, actual xdr.ScVal) (match bool, typeMismatch bool) {
	expInt, expIsInt := scval.ToBigInt(expected)
	actInt, actIsInt := scval.ToBigInt(actual)

	if expIsInt || actIsInt {
		if expIsInt && actIsInt {
			return expInt.Cmp(actInt) == 0, false
		}
		return false, true
	}

	if expected.Type != actual.Type {
		return false, true
	}

	expBytes, err1 := expected.MarshalBinary()
	actBytes, err2 := actual.MarshalBinary()
	if err1 == nil && err2 == nil {
		return bytes.Equal(expBytes, actBytes), false
	}

	return reflect.DeepEqual(expected, actual), false
}

func formatVal(v any) string {
	if v == nil {
		return "(void)"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func needsExtension(entry EntryReport) bool {
	if !entry.Present || entry.LiveUntilLedger == nil {
		return false
	}
	return entry.Status == health.StatusWarning || entry.Status == health.StatusCritical
}

func (p *Prober) evaluateRentPayer(ctx context.Context, req CheckRequest, inspect *InspectResult) (Check, *RentPayerReport, error) {
	acctKey, err := stellar.AccountKey(req.RentPayer)
	if err != nil {
		return Check{}, nil, err
	}
	encodedAcctKey, err := stellar.EncodeKey(acctKey)
	if err != nil {
		return Check{}, nil, err
	}

	contract, err := stellar.ParseContractID(req.ContractID)
	if err != nil {
		return Check{}, nil, err
	}

	var expiringEntries int
	var estimatedRent int64

	addRent := func(key xdr.LedgerKey, entry EntryReport) error {
		if !needsExtension(entry) {
			return nil
		}
		extendTo := inspect.LatestLedger + entry.Thresholds.Warn
		if entry.LiveUntilLedger != nil && *entry.LiveUntilLedger >= extendTo {
			extendTo = *entry.LiveUntilLedger + entry.Thresholds.Warn
		}
		fee, _, err := p.estimateExtendFee(ctx, key, extendTo)
		if err != nil {
			return err
		}
		expiringEntries++
		estimatedRent += fee
		return nil
	}

	if err := addRent(stellar.InstanceKey(contract), inspect.Instance); err != nil {
		return Check{}, nil, err
	}
	if inspect.Code != nil && inspect.WasmHash != "" {
		rawHash, err := hex.DecodeString(inspect.WasmHash)
		if err == nil && len(rawHash) == len(xdr.Hash{}) {
			var hash xdr.Hash
			copy(hash[:], rawHash)
			if err := addRent(stellar.CodeKey(hash), *inspect.Code); err != nil {
				return Check{}, nil, err
			}
		}
	}
	durability := req.DataDurability
	if durability == "" {
		durability = health.DurabilityPersistent
	}
	dataKeys, err := p.encodeDataKeys(contract, req.DataKeys, durability)
	if err != nil {
		return Check{}, nil, err
	}
	for i, dk := range dataKeys {
		if i >= len(inspect.Data) {
			break
		}
		if err := addRent(dk.key, inspect.Data[i]); err != nil {
			return Check{}, nil, err
		}
	}

	resp, err := p.client.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: []string{encodedAcctKey}})
	if err != nil {
		return Check{}, nil, err
	}

	report := &RentPayerReport{
		AccountID:       req.RentPayer,
		ExpiringEntries: expiringEntries,
		EstimatedRent:   estimatedRent,
	}
	if len(resp.Entries) == 0 {
		return Check{
			Name:    "rent_payer",
			Outcome: OutcomeFail,
			Detail:  fmt.Sprintf("rent payer account %s not found on ledger (estimated rent %d stroops for %d expiring entries)", req.RentPayer, estimatedRent, expiringEntries),
		}, report, nil
	}

	data, err := stellar.DecodeEntryData(resp.Entries[0].DataXDR)
	if err != nil {
		return Check{}, nil, err
	}
	if data.Account == nil {
		return Check{}, nil, fmt.Errorf("ledger entry for %s is not an account (got %s)", req.RentPayer, data.Type)
	}
	acc := data.Account

	numSub := uint32(acc.NumSubEntries)
	numSponsoring := uint32(acc.NumSponsoring())
	numSponsored := uint32(acc.NumSponsored())
	selling := int64(acc.Liabilities().Selling)

	reserveUnits := int64(2) + int64(numSub) + int64(numSponsoring) - int64(numSponsored)
	if reserveUnits < 0 {
		reserveUnits = 0
	}
	reserve := reserveUnits*baseReserveStroops + selling
	balance := int64(acc.Balance)
	spendable := balance - reserve
	if spendable < 0 {
		spendable = 0
	}

	report.Exists = true
	report.BalanceStroops = balance
	report.ReserveStroops = reserve
	report.SpendableStroops = spendable
	report.NumSubEntries = numSub
	report.NumSponsoring = numSponsoring
	report.NumSponsored = numSponsored
	report.SellingLiabilities = selling

	outcome := OutcomePass
	switch {
	case balance < reserve || spendable < estimatedRent:
		outcome = OutcomeFail
	case (estimatedRent > 0 && spendable < 2*estimatedRent) || (estimatedRent == 0 && spendable == 0):
		outcome = OutcomeWarn
	}

	detail := fmt.Sprintf(
		"account %s: balance %d - reserve %d (%d base reserves) = %d spendable stroops; estimated rent %d stroops for %d expiring entries",
		req.RentPayer, balance, reserve, reserveUnits, spendable, estimatedRent, expiringEntries,
	)
	return Check{
		Name:    "rent_payer",
		Outcome: outcome,
		Detail:  detail,
	}, report, nil
}
