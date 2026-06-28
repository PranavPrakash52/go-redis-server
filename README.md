# go-redis-server

A from-scratch, Redis-like server written in Go. This is a personal learning
project — the goal is to understand **database internals** (networking, the
RESP protocol, in-memory storage, key expiration) and pick up **Go language
fundamentals** along the way.

## Features

- **Async TCP server** using OS-level event multiplexing:
  - `kqueue` on macOS (`server/async_tcp_darwin.go`)
  - `epoll` on Linux (`server/async_tcp_linux.go`)
- **RESP (REdis Serialization Protocol)** parser and encoder (`core/resp.go`)
- **In-memory key-value store** with TTL support (`core/store.go`)
- **Active expiry sampling** — periodically samples random keys and deletes
  expired ones; if more than a configurable percentage of the sample is
  expired, it keeps sampling (mimics Redis' active expiry cycle)
- **CLI flags** for host/port configuration

## Supported commands

| Command | Notes |
|---------|-------|
| `PING`  | Returns `PONG` |
| `SET key value [EX seconds]` | Optional TTL in seconds |
| `GET key` | Returns value or nil |
| `DEL key [key ...]` | Returns number of keys removed |
| `EXPIRE key seconds` | Sets a TTL on an existing key |
| `TTL key` | Returns remaining TTL in seconds (`-1` no TTL, `-2` no key) |

## Getting started

```sh
# Build and run (defaults to 0.0.0.0:6379)
go run .

# Or with custom host/port
go run . --host 127.0.0.1 --port 6380
```

Then connect with the `redis-cli`:

```sh
redis-cli -p 6379
> SET foo bar
> GET foo
> SET temp hello EX 10
> TTL temp
```

## Project structure

```
.
├── main.go              # entry point, flag parsing
├── config/              # global config (host, port, cron interval, sample size)
├── server/              # TCP server implementations (sync + async per-OS)
└── core/                # RESP codec, command eval, and the in-memory store
```

## Learning notes

This project is intentionally minimal and built for exploration. Code is
written to be readable rather than production-grade — expect rough edges,
TODOs, and things that evolve as concepts get revisited.
