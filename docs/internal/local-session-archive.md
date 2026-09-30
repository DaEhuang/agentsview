# Local session archives

Status: proposed first delivery. Import, archive reparse commands, and recovery
verification are not implemented. The accompanying integration test exercises
Claude and Codex vault materialization through the existing local sync engine.

## Outcome

Keep original agent files and the existing AgentsView database when retiring a
machine. Browse the retained history immediately, then explicitly reparse
selected sessions from those original files. Moving the archive must preserve
session IDs, machine attribution, curation, and source identity.

Start with one originating device, immutable imports, and a local filesystem
vault. Keep a verified native backup independently of this feature. Retirement
must not depend on completing a new archive subsystem.

## How this fits the existing sync features

| Mechanism                                   | What it owns                                                                           | Use in this delivery                                                                                         |
| ------------------------------------------- | -------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------ |
| [Artifact folder sync](../artifact-sync.md) | Exchange of normalized sessions through a trusted folder                               | Keep its current contract. Its manifests do not retain original provider files or exchange mutable curation. |
| [Hosted raw sync](../hosted-raw-sync.md)    | Original source objects, canonical manifests, PostgreSQL acceptance and server parsing | Reuse the raw manifest format, provider capture plans, object-store adapter, and verified materializer.      |
| Local sync engine                           | Provider parsing and SQLite publication                                                | Use its normal content policy, message identities, signals, secret scanning, and large-Codex staging.        |
| Embedded Docbank                            | Immutable content and exclusive vault ownership                                        | Use a dedicated raw vault, separate from normalized artifact exchange.                                       |
| Local acceptance records                    | Original device/root identities, accepted generations, and their derived sessions      | New persistent state in `sessions.db`; not an upload checkpoint or a second session database.                |

Artifact folder sync is implemented but deliberately limited: no watcher,
schedule, hosted peer, or object-storage transport. Completing those features
does not make its normalized manifests a raw-source backup.

Hosted raw sync is the closer source-retention model. Its PostgreSQL leases,
fenced publication, server budgets, and authentication are unnecessary for a
serial local importer. `rawderive.ProviderParser` collects results in memory;
using it as the local write path would bypass the engine's large-Codex staging.
Reuse its source matching and stable-path preparation without copying its
buffered publication model.

## First delivery: import and explicit reparse

Seed the destination from a final consistent snapshot of the main AgentsView
database. Preserve older or alternate databases as named recovery files. Do not
union their histories or claim that their unique curation has been merged.

The import input supplies a capture inventory and an explicit original-device
and configured-root mapping. Persist those IDs independently of physical mount
paths. Repeating the import or binding the same root at a new path must resolve
to the same source chain. Preserve existing checkpoint IDs and receipts when
available; otherwise explicitly start a new import chain.

The first command imports an immutable, closed capture, not a live provider
directory. Validate capture hashes and use provider-owned capture plans for
Claude and Codex. Stream objects directly into the vault. Do not send the local
import through `rawcapture.Capturer` and its upload spool: the default spool is
1 GiB and retains a second full copy while uploading.

Accept one source in this order:

1. Store and verify its objects, then store its canonical raw manifest.
1. In one SQLite transaction, validate the expected source head and record the
   accepted generation, receipt, and requested reparse state.
1. Report custody independently from parsing. A parser failure must not erase
   acceptance or replace the previous browsable session.

Retries return the existing acceptance result. A competing head stops that
source and reports the conflict. Preserve the conflicting import as evidence
without silently re-parenting it. The existing upload client's automatic
re-parenting is not this policy.

Maintain a complete file inventory alongside the provider-source records. Store
files outside capture plans as supplemental objects with their relative paths,
hashes, and capture methods. This includes other provider trees, companion
history, configuration, and alternate database snapshots. Report separately:
bytes retained, provider source complete, and reparse verified. Unsupported
reparsing does not imply that the bytes or seeded sessions were discarded.

Existing raw manifests limit metadata to 1 MiB, entries to 4,096, object
references to 16,384, and each logical file to 16 GiB. Keep those limits
explicit. Preserve oversized source files as supplemental content and report the
reparse gap; do not accept incomplete provider sources to get around a limit.

### Reparse through the local engine

The owner selects accepted generations explicitly. Materialize and verify their
files, validate provider membership, and supply only those sources to the sync
engine. Disable ambient filesystem project discovery. Use the original device's
recorded machine label, stable source paths, and a resolver limited to the
materialized files.

`PathRewriter` must work independently of `IDPrefix`: importing an existing
device's archive does not rename its sessions. The regression test deletes the
original source tree, reads from the embedded vault, and checks native IDs,
machine attribution, stored source identity, and message content. It covers
Claude and collecting and staged Codex parses. This proves the component path,
not the unimplemented command or its durable acceptance contract.

For publication, run parsing into an isolated scratch database with the target's
content policy. Then atomically publish the selected source's derived sessions
and processing record through the local database write rules. Recheck the
accepted generation and existing session ownership inside that transaction.
Preserve existing message IDs and curation when reconciling with the seed. An
ambiguous match or divergent owner is a reported conflict, not a merge.

The scratch-to-live publication adapter is new work. Do not point
`SyncPathsContext` at the live archive and assume a later error rolls back every
session it wrote. Do not route raw-derived sessions through artifact import:
that API has peer-origin ownership and global-ID requirements of its own. Keep
large message bodies out of whole-session in-memory transfers.

Run serially under the archive writer, with persistent pending/error state for
restart. No worker leases or distributed queue are needed. Preflight scratch
space and expose progress. Hosted input/output budgets remain hosted limits.

### Startup and ownership

A parser-version change can already trigger a full resync at startup. Reparse
live sources as today, but carry archive-only sessions forward unchanged with
their curation, message identities, source proof, and processing version. Copy
the acceptance and binding records before the replacement database is installed.
Do not schedule the whole raw vault at startup; archived sources await an
explicit reparse request.

Only one process owns the raw vault. Normal full resync carries SQLite records
forward without opening it. If an explicit archive operation runs in the sync
worker, the parent closes the vault before handoff and reopens it only after the
worker closes. Extend the existing owner handoff rather than opening the same
vault independently from both processes.

## Recovery and large files

Use a stopped-owner filesystem copy first. Stop the daemon and workers and close
SQLite and Docbank for the entire copy. Include `sessions.db`, remaining SQLite
sidecars, the complete raw vault, `assets`, and required binding/configuration
files. Configuration and transcripts can contain credentials; preserve that
sensitivity in the recovery inventory.

Write lengths, hashes, component versions, and accepted-head digests into a
recovery manifest, then verify the destination by reading it back. Restore to an
independent empty directory. Check SQLite integrity and every accepted
manifest's complete object closure before reporting a usable recovery point. The
vault ID alone cannot distinguish a current vault from an older restored copy.
Embedded Docbank's bounded `Verify` call is not this complete check.

Kit [PR #132](https://github.com/kenn-io/kit/pull/132) adds chunked logical
objects to portable backup while retaining bounded pack entries. Docbank must
adopt that implementation and its matching larger admission limit before
AgentsView relies on it for large supplemental files or database snapshots.
Updating Kit alone does not lift Docbank's admission limit. Keep earlier
recovery repositories and their matching binaries intact; do not upgrade them
during this implementation.

Retain all accepted generations initially. Do not enable automatic deletion,
pruning, or text extraction in the raw vault. Docbank provenance is not a
garbage-collection pin; reachability must be designed before reclamation.

## Subsequent work

Continuous capture follows the importer. First move configured-root identity and
head authority out of the path-keyed `rawcheckpoint` database. It is not
currently disposable. Then bound staging space and avoid a full generation and
reparse for every Codex source whenever the shared session index changes. Add a
scale check where unrelated index updates do not schedule all unchanged sources.

Team delivery uses one archive per team, with project access controls covering
raw files and shared containers as well as derived sessions. Cross-device
identity and curation merging, cloud placement, coordinated online backup, and
retention are separate work. Keep them linked to the existing
[hosted raw-sync roadmap](https://github.com/kenn-io/agentsview/issues/1352).

## Release evidence still required

- Repeat an import after restart and after a mount-path change, without new IDs
  or duplicate chains.
- Reparse representative large Claude/Codex sources with original paths absent;
  preserve curation, message IDs, and the original machine attribution.
- Fail or cancel parsing before publication; keep the previous browsable result
  and the accepted raw generation.
- Exercise the actual startup resync path; preserve acceptance and archive-only
  history without scanning the vault.
- Restore a stopped-owner copy, including assets and supplemental files; reject
  a stale vault paired with a newer acceptance database.

The first implementation boundary is import, acceptance, explicit reparse,
resync preservation, and verified stopped-owner recovery. It does not include
continuous capture or a new team service.
