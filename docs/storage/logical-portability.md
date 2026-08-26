# Logical Portability

Logical Format v1 provides selective cross-instance portability. It is
implemented in the internal Go package `internal/portability`, not as a `dmemd`
command or HTTP endpoint. It is distinct from SQLite backup and restore: backup
replaces a complete data directory at an exact schema version, while logical
import maps selected domain objects into an existing target space.

## Package

A package contains `manifest.json`, `checksums.json`, and deterministic JSONL
members for spaces, raw events, memories, revisions, evidence, entities,
mentions, relations, conflicts, feedback, and aggregate provenance. FTS,
embeddings, vector data, derived feedback aggregates, jobs, leases, secrets, and
logs are not exported.

Selection names spaces and whether ancestors are included. The exporter closes
over required revisions, evidence, and raw events while representing references
outside the selection as stubs. Sensitive data follows the explicit export
policy. `Exporter.DryRun` reports counts, estimated size, sensitivity, external
references, and refusal reasons.

## Export Consistency

`Exporter.Export` reads one SQLite snapshot, writes a private temporary package,
calculates checksums, self-validates, syncs it, and atomically publishes a new
destination directory. Existing destinations, unsafe ownership or permissions,
symlinks, broken closure, and checksum failures are rejected.

## Import

`Importer.DryRun` strictly validates members, size limits, checksums, references,
format version, target space, deterministic ID mappings, duplicates, and local
revision conflicts before authority is written. The default limits are 13
files, 100,000 records, 64 MiB per file, 512 MiB total, and batches of 500.

`Importer.Import` stores a durable operation and commits bounded batches. Each
batch atomically writes objects, source-to-local mappings, provenance, and its
checkpoint. A retry resumes after the last committed batch without duplicating
objects. Generation fencing prevents an old importer from advancing a recovered
operation. `Status` reports progress; `Cancel` stops later batches but retains
already committed, fully mapped objects.

Duplicate content maps to existing objects. A mapped local memory whose current
revision conflicts with imported content produces an evolution proposal instead
of overwriting authority. Successful import queues FTS, semantic, entity,
relation, and feedback-aggregate rebuild work, so health can remain degraded
until those jobs finish.

Logical portability does not provide arbitrary SQLite compatibility, preserve-ID
mode, synchronization, replication, remote transport, or all-or-nothing rollback
of already committed batches.
