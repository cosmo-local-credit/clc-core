# Pool activity: existing-schema fixes

Busy pools previously classified their complete history before paging and scanned
matched swaps once per transfer. Core now selects up to 128 complete transactions
per batch and joins matched transfer keys once. A small store loop continues
through empty filtered batches until the page is full or candidates end. Event
cursors, forward/backward pages and the five-second deadline stay in place.

The indexer schema does not retain the protocol charge. Its `pool_swap.fee` is
the retained pool fee. Auxiliary transfers can include token tax, so even one
extra transfer is insufficient evidence. Activity omits `protocolFee`; omission
means unknown, not zero. This fix requires no indexer code or schema changes.

Withdrawal matching checks a suffix of pool outflows whose total equals the
operation's nominal amount, without crossing an earlier pool operation. This
retains the withdrawal kind when the recipient gets 90 after a nominal debit of
100. `transfer.amount` remains the matched amount and `nominalAmount` records the
operation amount. Initiator-only rows have direction `NONE` and nominal amounts.

## Existing requirements

Core still needs federation access to `pool_withdrawal` from the existing indexer
migration 007. This work does not apply that migration, modify the indexer, or
verify live event coverage. Do not deploy the query without that existing table.
No migration 008 or new `pool_swap` foreign columns are required.

## Verification

Tests use the existing swap schema, without settlement columns. They cover busy
and sparse accounts over PostgreSQL FDW, forward/backward pagination, a cursor
inside a transaction, filtered results after the first batch, tax ambiguity,
nominal/net withdrawals and operation boundaries.

Run only against an isolated test database; the helpers apply Core migrations:

```sh
CORE_TEST_DSN="$isolated_test_dsn" CORE_FDW_TEST=1 go test -count=1 -p 1 ./internal/store ./internal/api
go vet ./internal/store ./internal/api ./docs
```
