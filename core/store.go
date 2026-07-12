package core

import (
	"go-redis-server/config"
	"time"
)

var store map[string]*Obj

type Obj struct {
	Value        interface{}
	ExpiresAt    int64
	TypeEncoding uint8
}

func init() {
	store = make(map[string]*Obj)
}

func NewObj(value interface{}, durationMs int64, typ, enc uint8) *Obj {
	var expiresAt int64 = -1
	if durationMs > 0 {
		expiresAt = time.Now().UnixMilli() + durationMs
	}

	return &Obj{
		Value:        value,
		ExpiresAt:    expiresAt,
		TypeEncoding: SetTypeEncoding(typ, enc),
	}
}

func Put(k string, obj *Obj) {
	if len(store) >= config.MaxKeys {
		evict()
	}
	store[k] = obj
}

func Get(k string) *Obj {
	v := store[k]
	if v != nil {
		if v.ExpiresAt != -1 && v.ExpiresAt <= time.Now().UnixMilli() {
			delete(store, k)
			return nil
		}
	}
	return v
}

func Del(k string) int {
	v := store[k]
	if v != nil {
		delete(store, k)
		return 1
	}
	return 0
}

func ClearExpired() {
	for {
		// Pick up to SampleSize keys from the store.
		// Note: Go does NOT shuffle map keys — it randomizes only the
		// starting offset of iteration. The keys sit in a fixed layout
		// (per bucket/slot), so each range yields a random *rotation* of
		// that fixed order, and the first SampleSize keys form a random
		// contiguous window of it — not an independent random sample.
		// That's fine for active expiry (Redis does similar), and the
		// offset changes on every re-range, so resampling still hits
		// different keys across iterations.
		keys := make([]string, 0, config.SampleSize)
		for k := range store {
			keys = append(keys, k)
			if len(keys) >= config.SampleSize {
				break
			}
		}

		if len(keys) == 0 {
			return
		}

		now := time.Now().UnixMilli()
		deleted := 0

		for _, k := range keys {
			obj, ok := store[k]
			if !ok {
				continue
			}
			if obj.ExpiresAt != -1 && obj.ExpiresAt <= now {
				delete(store, k)
				deleted++
			}
		}

		// If more than ActiveExpireThresholdPercent% of the sampled keys were expired,
		// repeat the sampling to proactively reclaim memory under heavy TTL churn.
		// Expressed as a percentage so it stays correct regardless of SampleSize.
		if float64(deleted)/float64(len(keys)) <= float64(config.ActiveExpireThresholdPercent)/100 {
			return
		}
	}
}

func evict() {
	for key, _ := range store {
		delete(store, key)
		return

	}
}
