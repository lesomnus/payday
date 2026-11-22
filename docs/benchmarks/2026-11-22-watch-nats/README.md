# Three-replica NATS routing comparison

Run on 2026-11-22, Linux amd64, Go 1.27.3, NATS client v1.56.0 and server
v2.15.3. The server is embedded in the benchmark process and communicates over
loopback TCP. GOMAXPROCS is 4. The same host was also running repository checks;
these are diagnostic samples, not a production capacity estimate.

```sh
go test ./watch/watchnats -run '^$' \
  -bench BenchmarkThreeReplicaFanout -benchtime=1000x -count=3
```

One publisher sends the same single-change publication to three replica
connections. A and C want X; B wants Y. The broadcast case installs broad
interests on all three; the scoped case installs their actual topics. Both use
the same codec, payload, server and confirmed publication path. The benchmark
waits for all intended copies before the next publication, so it is not a
throughput or burst test. It compares NATS broadcast and NATS scoped delivery,
not NATS against PostgreSQL.

Medians of the three samples in [raw.txt](raw.txt):

| Measurement, per publication | Broadcast | Scoped |
| --- | ---: | ---: |
| Replica packets (NATS server OutMsgs) | 3 | 2 |
| Replica payload bytes (OutBytes) | 615 | 410 |
| Allocated bytes | 8,994 | 6,978 |
| Allocations | 103 | 84 |
| Elapsed time | 145,786 ns | 279,826 ns |
| Process CPU | 286,881 ns | 307,309 ns |

Packets and payload bytes fall by exactly one third in this topology, and
allocation counts fall. CPU and elapsed samples vary; they do **not** establish
a CPU or latency improvement. Process CPU includes the embedded NATS server
and all four connections; payload bytes exclude protocol/TCP overhead. A
production comparison needs an isolated host, deployment topology and the app's
actual interest distribution.

Separate regressions exercise the properties timing cannot establish:

- `TestThreeReplicasReceiveOnlyTheirInterests` checks B's connection packet
  counter, not just its filtered event channel. Temporarily replacing native
  interests with a broad subscription makes that assertion fail (0 vs 1 packet).
- `TestRefcountsAndDistinctMutationsWithOverlappingNativeInterests` checks native
  subscriptions are shared/released and two changes of the same row stay distinct.
- Notification's `TestInterpretationIsOncePerEventAndIndependentOfFirstReadersCancellation`
  checks one legacy resolver call across local readers. Generated notification
  changes carry their tenant/topic hint directly and need no interpretation lookup;
  policy reads remain per reader.
- The integration app's `TestNotifyNATSCrossReplicaListen` checks publication,
  private receipts and remote policy revocation through the real RPC surface.
