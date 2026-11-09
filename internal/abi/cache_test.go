package abi_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/abi"
)

type countingSource struct {
	calls atomic.Int32
	fn    func(network, contractID string) (*abi.Interface, error)
}

func (s *countingSource) Interface(_ context.Context, network, contractID string) (*abi.Interface, error) {
	s.calls.Add(1)
	return s.fn(network, contractID)
}

func TestCachingSourceHitsExpiryNegativeAndUncachedErrors(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		now = now.Add(d)
		clockMu.Unlock()
	}

	iface := &abi.Interface{Functions: []abi.Function{transfer}}

	t.Run("positive hit and expiry", func(t *testing.T) {
		fake := &countingSource{
			fn: func(string, string) (*abi.Interface, error) { return iface, nil },
		}
		cached := abi.NewCachingSource(fake, abi.CacheOptions{
			TTL:         5 * time.Minute,
			NegativeTTL: 30 * time.Second,
			MaxEntries:  4,
			Now:         clock,
		})

		got, err := cached.Interface(context.Background(), "testnet", contract)
		require.NoError(t, err)
		assert.Equal(t, iface, got)
		assert.Equal(t, int32(1), fake.calls.Load())

		got, err = cached.Interface(context.Background(), "testnet", contract)
		require.NoError(t, err)
		assert.Equal(t, iface, got)
		assert.Equal(t, int32(1), fake.calls.Load(), "second call within TTL is served from cache")

		// Different network is a separate key.
		_, err = cached.Interface(context.Background(), "public", contract)
		require.NoError(t, err)
		assert.Equal(t, int32(2), fake.calls.Load())

		advance(6 * time.Minute)
		_, err = cached.Interface(context.Background(), "testnet", contract)
		require.NoError(t, err)
		assert.Equal(t, int32(3), fake.calls.Load(), "expired entry is refetched")
	})

	t.Run("negative results are cached for shorter NegativeTTL", func(t *testing.T) {
		registered := false
		fake := &countingSource{
			fn: func(string, string) (*abi.Interface, error) {
				if !registered {
					return nil, fmt.Errorf("%w: not registered yet", abi.ErrUnknownContract)
				}
				return iface, nil
			},
		}
		cached := abi.NewCachingSource(fake, abi.CacheOptions{
			TTL:         5 * time.Minute,
			NegativeTTL: 30 * time.Second,
			Now:         clock,
		})

		_, err := cached.Interface(context.Background(), "testnet", contract)
		require.ErrorIs(t, err, abi.ErrUnknownContract)
		assert.Equal(t, int32(1), fake.calls.Load())

		// Within negative TTL, the negative result is served from cache.
		registered = true
		_, err = cached.Interface(context.Background(), "testnet", contract)
		require.ErrorIs(t, err, abi.ErrUnknownContract)
		assert.Equal(t, int32(1), fake.calls.Load())

		// Past negative TTL (31s < 5m positive TTL), it re-queries and picks up registration.
		advance(31 * time.Second)
		got, err := cached.Interface(context.Background(), "testnet", contract)
		require.NoError(t, err)
		assert.Equal(t, iface, got)
		assert.Equal(t, int32(2), fake.calls.Load())
	})

	t.Run("transport errors are never cached", func(t *testing.T) {
		fail := true
		fake := &countingSource{
			fn: func(string, string) (*abi.Interface, error) {
				if fail {
					return nil, errors.New("sorovault: connection refused")
				}
				return iface, nil
			},
		}
		cached := abi.NewCachingSource(fake, abi.CacheOptions{
			TTL: 5 * time.Minute,
			Now: clock,
		})

		_, err := cached.Interface(context.Background(), "testnet", contract)
		require.Error(t, err)
		assert.Equal(t, int32(1), fake.calls.Load())

		fail = false
		got, err := cached.Interface(context.Background(), "testnet", contract)
		require.NoError(t, err)
		assert.Equal(t, iface, got)
		assert.Equal(t, int32(2), fake.calls.Load(), "transport error was not cached")
	})

	t.Run("LRU eviction bounds cache size", func(t *testing.T) {
		fake := &countingSource{
			fn: func(string, string) (*abi.Interface, error) { return iface, nil },
		}
		cached := abi.NewCachingSource(fake, abi.CacheOptions{
			TTL:        5 * time.Minute,
			MaxEntries: 2,
			Now:        clock,
		})

		_, _ = cached.Interface(context.Background(), "testnet", "C1")
		_, _ = cached.Interface(context.Background(), "testnet", "C2")
		// Touch C1 so C2 is the least recently used.
		_, _ = cached.Interface(context.Background(), "testnet", "C1")
		_, _ = cached.Interface(context.Background(), "testnet", "C3")
		assert.Equal(t, int32(3), fake.calls.Load())

		// C1 is still cached; C2 was evicted.
		_, _ = cached.Interface(context.Background(), "testnet", "C1")
		assert.Equal(t, int32(3), fake.calls.Load())
		_, _ = cached.Interface(context.Background(), "testnet", "C2")
		assert.Equal(t, int32(4), fake.calls.Load())
	})
}

func TestCachingSourceConcurrentAccess(t *testing.T) {
	t.Parallel()

	iface := &abi.Interface{Functions: []abi.Function{transfer}}
	fake := &countingSource{
		fn: func(_ string, id string) (*abi.Interface, error) {
			if id == "C_UNKNOWN" {
				return nil, abi.ErrUnknownContract
			}
			return iface, nil
		},
	}
	cached := abi.NewCachingSource(fake, abi.CacheOptions{
		TTL:         time.Minute,
		NegativeTTL: 10 * time.Second,
		MaxEntries:  8,
	})

	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				target := fmt.Sprintf("C%d", (id+i)%10)
				if i%5 == 0 {
					target = "C_UNKNOWN"
				}
				_, _ = cached.Interface(context.Background(), "testnet", target)
			}
		}(g)
	}
	wg.Wait()
	assert.Positive(t, fake.calls.Load())
}
