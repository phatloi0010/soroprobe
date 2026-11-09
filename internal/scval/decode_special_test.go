package scval_test

import (
	"encoding/hex"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/scval"
)

func TestDecodeErrorValues(t *testing.T) {
	t.Parallel()

	t.Run("contract error includes numeric contract_code", func(t *testing.T) {
		t.Parallel()

		contractCode := xdr.Uint32(7)
		scErr := xdr.ScError{
			Type:         xdr.ScErrorTypeSceContract,
			ContractCode: &contractCode,
		}
		got, err := scval.Decode(xdr.ScVal{
			Type:  xdr.ScValTypeScvError,
			Error: &scErr,
		})
		require.NoError(t, err)
		assert.Equal(t, map[string]any{
			"type":          xdr.ScErrorTypeSceContract.String(),
			"contract_code": uint32(7),
		}, got)
	})

	t.Run("host error includes code name and no contract_code", func(t *testing.T) {
		t.Parallel()

		errCode := xdr.ScErrorCodeScecInvalidAction
		scErr := xdr.ScError{
			Type: xdr.ScErrorTypeSceValue,
			Code: &errCode,
		}
		got, err := scval.Decode(xdr.ScVal{
			Type:  xdr.ScValTypeScvError,
			Error: &scErr,
		})
		require.NoError(t, err)
		assert.Equal(t, map[string]any{
			"type": xdr.ScErrorTypeSceValue.String(),
			"code": xdr.ScErrorCodeScecInvalidAction.String(),
		}, got)
		decodedMap, ok := got.(map[string]any)
		require.True(t, ok)
		assert.NotContains(t, decodedMap, "contract_code")
	})

	t.Run("nil Error payload returns an error without panicking", func(t *testing.T) {
		t.Parallel()

		_, err := scval.Decode(xdr.ScVal{
			Type: xdr.ScValTypeScvError,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "error ScVal has no value")
	})
}

func TestDecodeContractInstance(t *testing.T) {
	t.Parallel()

	t.Run("wasm executable with storage map", func(t *testing.T) {
		t.Parallel()

		var hash xdr.Hash
		for i := range hash {
			hash[i] = byte(i + 1)
		}

		adminKey := xdr.ScSymbol("Admin")
		adminVal := xdr.ScString("alice")
		countKey := xdr.ScSymbol("Count")
		countVal := xdr.Uint32(42)
		storage := xdr.ScMap{
			{
				Key: xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &adminKey},
				Val: xdr.ScVal{Type: xdr.ScValTypeScvString, Str: &adminVal},
			},
			{
				Key: xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &countKey},
				Val: xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &countVal},
			},
		}

		instance := xdr.ScContractInstance{
			Executable: xdr.ContractExecutable{
				Type:     xdr.ContractExecutableTypeContractExecutableWasm,
				WasmHash: &hash,
			},
			Storage: &storage,
		}

		got, err := scval.Decode(xdr.ScVal{
			Type:     xdr.ScValTypeScvContractInstance,
			Instance: &instance,
		})
		require.NoError(t, err)
		assert.Equal(t, map[string]any{
			"executable": xdr.ContractExecutableTypeContractExecutableWasm.String(),
			"wasm_hash":  hex.EncodeToString(hash[:]),
			"storage": map[string]any{
				"Admin": "alice",
				"Count": uint32(42),
			},
		}, got)
	})

	t.Run("nil Instance payload returns an error without panicking", func(t *testing.T) {
		t.Parallel()

		_, err := scval.Decode(xdr.ScVal{
			Type: xdr.ScValTypeScvContractInstance,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "contract instance ScVal has no value")
	})

	t.Run("malformed storage entry propagates error", func(t *testing.T) {
		t.Parallel()

		badKey := xdr.ScSymbol("Broken")
		storage := xdr.ScMap{
			{
				Key: xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &badKey},
				Val: xdr.ScVal{Type: xdr.ScValTypeScvU32}, // nil U32 pointer
			},
		}
		instance := xdr.ScContractInstance{
			Executable: xdr.ContractExecutable{
				Type: xdr.ContractExecutableTypeContractExecutableStellarAsset,
			},
			Storage: &storage,
		}

		_, err := scval.Decode(xdr.ScVal{
			Type:     xdr.ScValTypeScvContractInstance,
			Instance: &instance,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "instance storage")
	})
}
