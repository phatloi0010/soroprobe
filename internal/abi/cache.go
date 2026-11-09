package abi

import (
	"container/list"
	"context"
	"errors"
	"sync"
	"time"
)

const (
	// DefaultCacheTTL is the default lifetime of a positive SoroVault interface lookup.
	DefaultCacheTTL = 5 * time.Minute
	// DefaultNegativeCacheTTL is the default lifetime of a negative lookup (ErrUnknownContract).
	DefaultNegativeCacheTTL = 30 * time.Second
	// DefaultCacheMaxEntries bounds the number of cached (network, contractID) entries.
	DefaultCacheMaxEntries = 256
)

// CacheOptions configures a CachingSource.
type CacheOptions struct {
	// TTL is how long a positive interface lookup is kept. Defaults to DefaultCacheTTL.
	TTL time.Duration
	// NegativeTTL is how long an ErrUnknownContract lookup is kept. Defaults to
	// DefaultNegativeCacheTTL (or a fraction of TTL when TTL is shorter).
	NegativeTTL time.Duration
	// MaxEntries is the maximum number of cached (network, contractID) pairs.
	// Defaults to DefaultCacheMaxEntries.
	MaxEntries int
	// Now returns the current time. Defaults to time.Now; tests may override it.
	Now func() time.Time
}

type cacheKey struct {
	network    string
	contractID string
}

type cacheEntry struct {
	key       cacheKey
	iface     *Interface
	err       error
	expiresAt time.Time
}

// CachingSource decorates a Source with a bounded, TTL-expiring LRU cache
// keyed by (network, contractID). Negative results (ErrUnknownContract) are
// cached for a shorter duration; transport and server errors are never cached.
type CachingSource struct {
	src         Source
	ttl         time.Duration
	negativeTTL time.Duration
	maxEntries  int
	now         func() time.Time

	mu    sync.Mutex
	order *list.List
	items map[cacheKey]*list.Element
}

var _ Source = (*CachingSource)(nil)

// NewCachingSource wraps src in a concurrent-safe LRU cache.
func NewCachingSource(src Source, opts CacheOptions) *CachingSource {
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	negTTL := opts.NegativeTTL
	if negTTL <= 0 {
		if DefaultNegativeCacheTTL < ttl {
			negTTL = DefaultNegativeCacheTTL
		} else {
			negTTL = max(ttl/5, time.Millisecond)
		}
	}
	maxEntries := opts.MaxEntries
	if maxEntries <= 0 {
		maxEntries = DefaultCacheMaxEntries
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &CachingSource{
		src:         src,
		ttl:         ttl,
		negativeTTL: negTTL,
		maxEntries:  maxEntries,
		now:         now,
		order:       list.New(),
		items:       make(map[cacheKey]*list.Element, maxEntries),
	}
}

// Interface returns the cached interface when fresh, or queries the underlying
// Source and caches positive and ErrUnknownContract results.
func (c *CachingSource) Interface(ctx context.Context, network, contractID string) (*Interface, error) {
	key := cacheKey{network: network, contractID: contractID}
	if iface, err, ok := c.get(key); ok {
		return iface, err
	}

	iface, err := c.src.Interface(ctx, network, contractID)
	if err != nil {
		if errors.Is(err, ErrUnknownContract) {
			c.put(key, nil, err, c.negativeTTL)
		}
		return nil, err
	}

	c.put(key, iface, nil, c.ttl)
	return iface, nil
}

func (c *CachingSource) get(key cacheKey) (*Interface, error, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.items[key]
	if !ok {
		return nil, nil, false
	}
	entry := elem.Value.(*cacheEntry)
	if !c.now().Before(entry.expiresAt) {
		c.order.Remove(elem)
		delete(c.items, key)
		return nil, nil, false
	}
	c.order.MoveToFront(elem)
	return entry.iface, entry.err, true
}

func (c *CachingSource) put(key cacheKey, iface *Interface, err error, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	expiresAt := c.now().Add(ttl)
	if elem, ok := c.items[key]; ok {
		entry := elem.Value.(*cacheEntry)
		entry.iface = iface
		entry.err = err
		entry.expiresAt = expiresAt
		c.order.MoveToFront(elem)
		return
	}

	if c.order.Len() >= c.maxEntries {
		oldest := c.order.Back()
		if oldest != nil {
			c.order.Remove(oldest)
			delete(c.items, oldest.Value.(*cacheEntry).key)
		}
	}

	entry := &cacheEntry{
		key:       key,
		iface:     iface,
		err:       err,
		expiresAt: expiresAt,
	}
	elem := c.order.PushFront(entry)
	c.items[key] = elem
}
