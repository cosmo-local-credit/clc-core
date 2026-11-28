# Roll out editable Voucher symbols

CLC display symbols and immutable deployment symbols are separate. Existing
and legacy clients keep the deployment symbol on the wire. Current clients
use the display symbol across resource and payment views. Never change
`vouchers.symbol` when editing or backfilling a display symbol.

## Core then clients

1. Back up the target database. Check migration 087 on an isolated copy
   restored from that environment and on the staging PostgreSQL image. The
   migration adds nullable columns and new claims. It does not rename data or
   require PostGIS. Confirm Core health checks after migrating.
2. With Core deployed, produce the **read-only** audit on the intended database
   using an authorized database account. Set `CORE_VOUCHER_SYMBOLS_DSN` in the
   shell through the normal secret-management workflow.
3. Review every audit row and record the approved mappings. Keep each
   `suggestedDisplaySymbol` for a valid and unique legacy label. Assign a fresh
   distinct label to **every** active Voucher in a collision, including a
   collision with a Pool or pending deployment. Confirm any invalid legacy
   label with its issuer. Never resolve a collision by awarding the old label
   to one member. Already assigned, non-conflicting labels need no mapping.
4. Apply the complete reviewed file in a quiet window. The command verifies
   the expected old values, locks the participating tables briefly and
   attempts the entire mapping in one transaction. Existing uniqueness
   constraints apply. If the catalog changed or a lock timed out, run another
   audit and refresh the review. No partial changes are committed.
5. Confirm `--check` prints `[]`, then deploy Web and Admin. Verify create,
   rename, retire, reuse and restore on staging, including newer and old
   clients. Check display labels in Wallet, marketplace search, and payments.

```sh
go run ./cmd/voucher-symbols > voucher-symbol-audit.json
go run ./cmd/voucher-symbols --apply reviewed-voucher-symbols.json
go run ./cmd/voucher-symbols --check
```

Example reviewed JSON entry (fictitious):

```json
[
  {
    "chainId": 100,
    "address": "0x0000000000000000000000000000000000000001",
    "expectedContractSymbol": "SAMP",
    "expectedDisplaySymbol": null,
    "displaySymbol": "SAMP2"
  }
]
```

The audit is for **active** Vouchers. A retired Voucher keeps an assigned
label for recovery/history but does not reserve it. A retired legacy record
without a separate label acquires its normalized native ticker as the CLC
label if it is valid and available. Otherwise supply a valid replacement on
restoration. Native deployment identities stay reserved independently.

## Require a saved display symbol after both backfills

Keep this step out of the initial schema 087 rollout. First deploy a Core
build that preserves the saved display symbol on legacy deployment replay.
Finish the reviewed active backfill independently in staging and production.
For any retired Voucher still missing a label, use an explicit chain/address
mapping in a transaction guarded by the expected old values and retired
status. Copy its valid historical label, or have a replacement reviewed if
the old ticker is invalid. The retired row does not reserve the label.

Before enabling the requirement, take a fresh read-only readiness report
for **both** databases:

```sh
go run ./cmd/voucher-symbols --check-required
```

The report must be `[]` in both environments. In addition to active
collisions and missing catalog labels, it checks unresolved labels on
unpromoted deployments and actionable queue jobs. Resolve those through the
existing deployment process before enabling the requirement. Use the
reported tracking/job identifiers and consult the operator responsible for
the deployment before changing a queued job or a submitted transaction.

Run the following during a quiet window against staging first, then
production after staging passes. The command uses the intended database
from `CORE_VOUCHER_SYMBOLS_DSN`, rechecks readiness with catalog writes and
deployment processing locked, then sets `NOT NULL` on
`vouchers.display_symbol` in the same transaction. If the database is busy,
the command exits without queuing behind a transaction. Retry in a quiet
window. It leaves deployment history nullable and never rewrites or infers
catalog labels. Core startup does not run this command.

```sh
go run ./cmd/voucher-symbols --enforce-required
go run ./cmd/voucher-symbols --check-required
```

Confirm normal create/edit and retirement/reuse/restore. After restoring
a database from an earlier snapshot, repeat the readiness and enforcement
steps as needed. The readiness report alone does not attest that the
constraint is installed. Keep schema
087 and the reviewed mappings in place. If the required-column change must
be reverted by an operator, only drop the `NOT NULL` attribute on
`vouchers.display_symbol`. Keep the stored labels and both claim namespaces.

Roll back client code first if necessary. After schema 087 is applied, use a
Core rollback build compatible with that migration. An 086-only image cannot
initialize a database that has applied 087. Retain the reviewed mapping and
do not roll back labels by rewriting contract symbols.
