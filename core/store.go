package core

import (
	"go-redis-server/config"
	"time"
)

var store map[string]*Obj

type Obj struct {
	Value     interface{}
	ExpiresAt int64
}

func init() {
	store = make(map[string]*Obj)
}

func NewObj(value interface{}, durationMs int64) *Obj {
	var expiresAt int64 = -1
	if durationMs > 0 {
		expiresAt = time.Now().UnixMilli() + durationMs
	}

	return &Obj{
		Value:     value,
		ExpiresAt: expiresAt,
	}
}

func Put(k string, obj *Obj) {
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
		// Pick up to sampleSize random keys from the store.
		// Go map iteration order is randomized, so the first N keys
		// encountered form an effectively random sample.
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
