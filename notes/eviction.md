# Building a Redis-style DB in Go - Notes

## 1. Why Does Redis Need Eviction?

**Q: Why can't Redis just keep storing keys forever?**

All keys in Redis are stored in **RAM (memory)**, and RAM is limited. If you keep inserting keys without any limit, you'll eventually hit a point where there's not enough memory to allocate. At that point:
- The Redis process will **crash** with an **OOM (Out Of Memory)** error.
- You lose your entire database because the process died.

You never want your database to crash due to OOM — that's a catastrophic failure.

**Q: So what does eviction do?**

Eviction is the mechanism that kicks in **before** you run out of memory. For example, if you hypothetically set a limit of **1 GB**:
- Redis keeps storing keys normally.
- When it hits that 1 GB limit and you try to set a **new** key, Redis **evicts (removes) some old data** to make space for the new key.
- Which key gets removed depends on the **eviction strategy** you configure.

This prevents OOM crashes and keeps the database running.

---

## 2. The `maxmemory` Configuration

**Q: How does Redis know when to start evicting?**

Redis has a configuration parameter called **`maxmemory`** in the `redis.conf` file. You set it like:
```
maxmemory 1gb
```

When Redis hits that limit, it starts evicting keys based on the **eviction policy** you've configured. The strategy determines *which* key is the "best candidate" to throw out so the new write can succeed.

---

## 3. The Different Eviction Strategies

**Q: What are all the eviction strategies Redis supports?**

Redis offers **8 different eviction strategies**. Let's go through each one with its pros and cons.

### 3.1 No Eviction
**Q: What happens with `noeviction`?**

When Redis hits the `maxmemory` limit, it **does not evict anything**. Any new write that comes in is simply **discarded** — the incoming writes are rejected.

- **Advantage:** No data is ever lost or removed by Redis itself. Predictable behavior.
- **Disadvantage:** New writes are silently dropped. Your application must handle write failures, otherwise data the client *thought* was saved is actually gone.

---

### 3.2 All Keys LRU
**Q: What is `allkeys-lru`?**

LRU = **Least Recently Used**. This is the classic cache eviction algorithm you may have studied in OS courses. Redis evicts the key that has **not been accessed for the longest time**.

The reasoning: *If a key hasn't been accessed recently (e.g., in the last 3 hours), there's a very low chance it'll be accessed again.*

- **Advantage:** Keeps the "hot" (frequently/recently used) keys in memory. Good general-purpose cache strategy.
- **Disadvantage:** Redis has to maintain some metadata about *when* each key was last accessed, which adds overhead.

---

### 3.3 All Keys LFU
**Q: What is `allkeys-lfu`?**

LFU = **Least Frequently Used**. Instead of tracking *recency*, Redis maintains a **frequency count** — how many times each key has been accessed. It evicts the key with the **lowest frequency count**.

- **Advantage:** Great when access is uneven — a key accessed 10,000 times is clearly more valuable than one accessed twice, even if the second was more recent.
- **Disadvantage:** Has to store and update a frequency counter for every key. (Redis solves this efficiently using a Morris counter — see section 5.)

**Q: When should you prefer LFU over LRU?**

Use LFU when you want to **hold onto a key** even if there's a temporary dip in its access pattern. Think of it like the stock market: even if a stock is currently *down*, if you believe in it (because it was accessed heavily in the past), you keep holding it. LFU respects historical frequency, not just recent activity.

---

### 3.4 Volatile LRU
**Q: How is `volatile-lru` different from `allkeys-lru`?**

It works exactly like LRU, but **only considers keys that have an expiration (TTL) set**. Keys with **no expiration set are never evicted** — they stay in memory permanently.

- **Advantage:** Lets you "pin" certain keys permanently in Redis. If you have critical keys that should never be evicted even when the cache is full, just don't set an expiry on them.
- **Disadvantage:** Only a subset of keys (those with TTL) is eligible for eviction, so the candidate pool is smaller. If most keys have no TTL, this strategy may not free up much space.

---

### 3.5 Volatile LFU
**Q: What is `volatile-lfu`?**

Same as `allkeys-lfu`, but only considers keys that have some expiration set. Keys without a TTL are untouched.

- **Advantage:** Same "pinning" benefit as volatile LRU, combined with the frequency-based logic of LFU.
- **Disadvantage:** Smaller eviction candidate pool (only keys with TTL).

---

### 3.6 All Keys Random
**Q: What is `allkeys-random`?** *(The speaker's personal favorite!)*

When the cache is full, Redis just **picks a key at random** and evicts it. No LRU, no LFU, no metadata — pure randomness.

- **Advantage:** **Zero extra data structure overhead.** No need to track access time or frequency for any key. Incredibly memory-efficient and fast.
- **Disadvantage:** You might accidentally evict a key that was *just* used and is still hot. No intelligence in the selection.

**Q: When does `allkeys-random` actually make sense?**

Use it when you have **uniform access patterns** across all keys. If every key is roughly equally likely to be accessed, there's no point maintaining LRU/LFU metadata — random eviction works just as well with way less overhead.

---

### 3.7 Volatile Random
**Q: What is `volatile-random`?**

Same as `allkeys-random`, but restricted to keys that have some expiration set. Keys without a TTL are preserved.

- **Advantage:** No metadata overhead + protection for permanent (no-TTL) keys.
- **Disadvantage:** Random (non-intelligent) eviction within the volatile subset.

---

### 3.8 Volatile TTL
**Q: What is `volatile-ttl`?**

Redis picks the key that has the **shortest Time To Live** — i.e., the key that is *about to expire soonest* anyway — and evicts it first.

- **Advantage:** Maximizes "useful" eviction — you're removing the key that was going to disappear on its own soonest anyway. Highly efficient.
- **Disadvantage:** Only works on keys with a TTL set; useless if your keys have no expiry.

---

## 4. How Redis *Actually* Implements LRU — Approximated LRU

**Q: Implementing LRU naively requires a doubly linked list. Does Redis really do that?**

No! That's the key insight. A textbook LRU implementation needs a **doubly linked list + hash map** to track access order across all keys. For Redis, that's **way too much memory overhead** — maintaining all those pointers would waste memory that could be used to store actual data.

**Q: So how does Redis do it instead?**

Redis uses **Approximated LRU** (introduced in Redis 3.0). The core idea is **sampling**:

1. Instead of tracking exact access order globally, each key stores a timestamp of when it was last accessed.
2. When eviction is needed, Redis doesn't scan all keys. It picks a **sample of N batches**, each containing **K keys** (both N and K are configurable).
3. From those sampled keys, it evicts the **single least-recently-used** one.

**Q: Why is this better than just picking one sample?**

If you only sample *one* batch of K keys, you might still pick a sub-optimal key to evict. But by taking **multiple samples (N batches)** and picking the LRU candidate across all of them, you dramatically increase the chances of getting **as close as possible to true LRU** behavior — without paying for the data-structure overhead.

**Q: Why doesn't Redis just use exact LRU?**

Because caches must be **extremely efficient**. Every byte spent on metadata (pointers, linked-list nodes, access logs) is a byte *not* used for storing user data. Redis would rather use that memory for actual data and rely on **approximation + sampling** — which is "good enough" in practice and incredibly space-efficient.

---

## 5. How Redis Implements LFU — The Morris Counter

**Q: LFU needs a frequency count per key. Isn't that an integer (4 bytes) per key? That sounds expensive.**

Exactly the problem! A naive implementation would store a 4-byte integer (capable of counting up to ~1 million) for *every single key*. With millions of keys, that's a huge amount of wasted memory, especially since most keys might only ever have a frequency of 2, 3, or 10.

**Q: What's the solution?**

Redis uses a **Morris Counter** (introduced with the new LFU mode in Redis 4.0) instead of a normal `int++`. A Morris counter is an **approximate counting** data structure based on probability — a classic example of a **probabilistic / approximate data structure**.

**Q: What's so special about the Morris counter?**

It lets you represent large values (like 1 million) using only **2–3 bytes** instead of 4. The exact count isn't preserved — it's *approximated* — but the relative ordering (which key is more/less frequent) stays accurate enough for eviction decisions.

Every byte saved per key, multiplied by millions of keys, frees up enormous memory for storing actual data.

*(For a deep mathematical dive on how Morris counters work — probability, logarithmic counting, the research paper — the speaker recommends their detailed blog post at **arpitbhayani.me/blog/morris-counter**.)*

---

## 6. The Decay Problem in LFU

**Q: What if a key was accessed heavily in the past but never accessed again? Should it stay forever just because its frequency count is high?**

No — that would defeat the purpose of eviction. Redis solves this with **logarithmic decay**:

- The frequency counter **caps at ~1 million** (it saturates and won't go higher).
- **Every minute**, the counter undergoes a **logarithmic decay** — its value is gradually reduced.
- So if a key was hot in the past but goes cold, its counter slowly decays toward **zero**, eventually making it eligible for eviction again.

**Q: Is the decay configurable?**

Yes — the saturation point and decay rate are both configurable in `redis.conf`. The default is decay applied every 1 minute. The math behind the decay function involves logarithms (not covered in detail in the transcript, but documented in Redis's official docs).

---

## 7. Summary Table: All Eviction Strategies

| Strategy             | Scope                          | Selection Logic              | Best Used When...                                       |
|----------------------|--------------------------------|------------------------------|---------------------------------------------------------|
| `noeviction`         | N/A (no eviction)              | Rejects new writes           | You never want Redis to remove data automatically.      |
| `allkeys-lru`        | All keys                       | Least recently used          | General-purpose caching with skewed recent access.      |
| `allkeys-lfu`        | All keys                       | Least frequently used        | Historical frequency matters more than recency.         |
| `volatile-lru`       | Keys with TTL only             | Least recently used          | You want to "pin" some keys permanently.                |
| `volatile-lfu`       | Keys with TTL only             | Least frequently used        | Frequency-based, with pinned permanent keys.            |
| `allkeys-random`     | All keys                       | Random                       | Uniform access patterns; minimal overhead needed.       |
| `volatile-random`    | Keys with TTL only             | Random                       | Random eviction, but protect permanent keys.            |
| `volatile-ttl`       | Keys with TTL only             | Shortest remaining TTL       | Evict keys about to expire anyway (most efficient).     |

---

## 8. The Two Key Takeaways

**Q: What are the two most important approximated algorithms behind Redis eviction?**

1. **Approximated LRU** (Redis 3.0) — uses **sampling** instead of a doubly linked list to find a near-LRU candidate, saving enormous memory.
2. **Approximated LFU via Morris Counter** (Redis 4.0) — uses **probabilistic counting** to store frequency in 2–3 bytes instead of 4, with logarithmic decay to age out cold keys.

Both reflect a single philosophy: **Redis prefers to spend memory on actual data, not on metadata — and relies on probabilistic approximation when "good enough" beats "perfect but expensive."**

---

## 9. Implementing a Simple Eviction Strategy (Hands-On)

**Q: Are we going to implement real LRU/LFU in our Go Redis clone?**

No — implementing them exactly as Redis does (with approximated sampling, Morris counters, decay, etc.) is too complex for this exercise. Instead, we'll implement an **extremely simple "Simple-First" eviction strategy** just to understand **where the eviction code would be placed** in our repository.

**Q: What does "Simple-First" eviction do?**

When the cache is full, it simply **evicts the first key it finds**. No intelligence, no sampling, no frequency tracking — just a placeholder to demonstrate the wiring of eviction logic into our existing SET/GET flow.

This gives us the structural understanding of where eviction hooks into a Redis-like server, so that swapping in a smarter strategy later is just a matter of replacing the selection function.
