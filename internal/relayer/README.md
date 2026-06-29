# relayer

EIP-7702 / Calibur gasless relayer. Accepts client-signed Calibur **batched**
calls and produces raw transactions for dispatch via the River `DISPATCH`
worker. The relayer never holds the master key — every signature is produced
inside a worker's `Work` transaction.

## Batched (multi-call) transactions

A relay payload carries a `SignedBatchedCall` whose `batchedCall.calls` is an
**array** (1–16 calls, signed once by the user). The motivating example —
`approve(0) + approve(amount) + transferFrom`/`pull` — is simply three entries
in that array; the user signs the whole batch once and the relayer submits it
in a single transaction, gaslessly. No per-call signatures, no extra round
trips. Both signing methods work unchanged:

- **secp256k1** root key (`keyHash == 0`)
- **WebAuthn-P256** passkey (`keyHash` derived from the public key)

### Gas

The first relay also delegates (an EIP-7702 `SetCodeTx`), and the Calibur code
is not live at the EOA until that tx executes, so `eth_estimateGas` would
revert. The limit is therefore budgeted from known per-action averages:

```
limit = (base_exec_gas + per_call_gas*len(calls) [+ delegation_overhead_gas]) * 1.10
```

clamped to `max_relay_gas`. Tune `per_call_gas` to the measured average gas of a
single action (see `[relayer]` in `config.toml`).

## Tests

- `make test` — hermetic unit tests (no chain): golden EIP-712 digest pinning
  Go↔dapp agreement, secp256k1 + WebAuthn signing round-trips, gas math, nonce
  packing.
- `make test-integration` — boots `anvil --hardfork prague` forking Celo (where
  Calibur is deployed at `0x1289…`), deploys mock contracts, and relays real
  multi-call batches end to end through `BuildAndSignRelay` → dispatch:
  - **relay 1**: first relay (`SetCodeTx`) — delegate + register a passkey +
    `approve(0)/approve/pull` in one 4-call batch.
  - **relay 2**: already-delegated (`DynamicFeeTx`) — `approve(0)/approve/pull`.

  Requires `anvil` (foundry) on `PATH`. Override the fork RPC with
  `CELO_FORK_RPC=<url>`; prefer a non-rate-limited node (the public forno
  endpoint can serve inconsistent fork state under load). Accounts are freshly
  generated each run and funded via `anvil_setBalance`, so CREATE addresses are
  deterministic and never collide with real Celo state.

`internal/relayer/testsign` builds and signs batches exactly as the dapp does
(secp256k1 and WebAuthn), reusable from any backend test.

An on-chain WebAuthn relay test (`TestIntegration_WebAuthnRelay`) is present but
skipped: the wrapped WebAuthn signature is produced correctly (covered
hermetically by the unit tests), but Calibur's on-chain WebAuthn verifier needs
a `clientDataJSON` byte layout matching a real passkey assertion — capture one
from the dapp and wire it into `testsign` to enable it.
