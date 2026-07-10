package proxy

import (
	"crypto/sha256"
	"sync"
	"time"
)

const (
	dedupTTL     = 5 * time.Minute
	dedupMaxSize = 128
	dedupMaxBody = 256 << 10 // only small standalone requests are worth replaying
)

type dedupEntry struct {
	body    []byte // complete Anthropic-shaped JSON response
	model   string
	inTok   int
	outTok  int
	expires time.Time
}

// dedupCache replays recent identical internal requests so a retried
// housekeeping call never pays twice. Keyed by the request body hash:
// identical bytes mean identical conversation state, so the same answer is
// valid. Only non-streaming, allowlisted-internal requests are eligible.
type dedupCache struct {
	mu sync.Mutex
	m  map[[32]byte]dedupEntry
}

func newDedupCache() *dedupCache { return &dedupCache{m: make(map[[32]byte]dedupEntry)} }

func dedupKey(body []byte) [32]byte { return sha256.Sum256(body) }

func (d *dedupCache) get(key [32]byte) (dedupEntry, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.m[key]
	if !ok {
		return dedupEntry{}, false
	}
	if time.Now().After(e.expires) {
		delete(d.m, key)
		return dedupEntry{}, false
	}
	return e, true
}

func (d *dedupCache) put(key [32]byte, e dedupEntry) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	if len(d.m) >= dedupMaxSize {
		for k, v := range d.m {
			if now.After(v.expires) {
				delete(d.m, k)
			}
		}
		for k := range d.m { // still full → drop arbitrary entries
			if len(d.m) < dedupMaxSize {
				break
			}
			delete(d.m, k)
		}
	}
	e.expires = now.Add(dedupTTL)
	d.m[key] = e
}
