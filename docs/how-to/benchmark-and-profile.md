# How to benchmark and profile the server

## Pick a benchmark shape

The Makefile has three presets that match common traffic:

```bash
make bench-put     # 50 × 64 MiB uploads, 8 workers, agent traffic
make bench-get     # 500 × 1 MiB downloads over 50 objects, 32 workers, human traffic
make bench-range   # 200 × 8 MiB random ranges from 256 MiB objects, 16 workers
```

Or run `ffbench` directly against any server:

```bash
./bin/ffbench -url https://files.example.com -op get -n 1000 -c 64 -size 5MiB -keys 100 -kind human
```

All flags are listed in the [ffbench reference](../reference/ffbench.md).

Run the benchmark from where your real clients are. A benchmark from a laptop next to the server tells you little about agents in another region.

## Test with compressible data

```bash
./bin/ffbench -op put -n 50 -size 64MiB -data text
./bin/ffbench -op put -n 50 -size 64MiB -data random
```

Use this to compare before and after you add compression.

## Test over HTTP/2

Start the server with `FILEFLOW_H2C=true`, then:

```bash
./bin/ffbench -h2c -op get -n 2000 -c 64 -size 64KiB -keys 100
```

## Track regressions in CI

```bash
./bin/ffbench -op get -n 500 -c 32 -size 1MiB -json > bench-$(git describe).json
```

Keep these files per release and diff the percentiles.

## Profile the CPU during a benchmark

In one terminal, start a benchmark long enough to cover 30 seconds. In another:

```bash
make profile
```

This takes a 30-second CPU profile from the admin port and opens the pprof web UI on `:7070`. Look for `sha256`, `crypto/tls` or `compress` near the top. Those mean the gateway's CPU is the limit.

Other profiles on the admin port:

```bash
go tool pprof http://localhost:9090/debug/pprof/heap
go tool pprof http://localhost:9090/debug/pprof/goroutine
curl -o trace.out "http://localhost:9090/debug/pprof/trace?seconds=5" && go tool trace trace.out
```

## Watch the metrics

```bash
curl -s localhost:9090/metrics | grep -e inflight -e go_goroutines -e heap_inuse
```

Or start Prometheus with the stack, where it is available on `:9091`:

```bash
docker compose --profile observability up
```

## Interpret the results

Look up the slowest trace ids that ffbench prints in the server log, and find the largest `timings_ms` phase. The [timing split explanation](../explanation/timing-split.md) says what each phase means. The table in [ROADMAP.md](../../ROADMAP.md) maps each one to a fix.
