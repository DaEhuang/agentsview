# Move a session archive without losing its origins

Status: proposed; requires review before implementation. This revises the
direction of [PR #2036](https://github.com/kenn-io/agentsview/pull/2036), not
the description of what that branch currently implements. The
[current implementation](../../internal/local-session-archive.md) remains the
baseline until the changes below are built and verified.

## Outcome and limits

A person retiring a computer can keep its original agent files, their existing
AgentsView history and curation, and durable evidence of which machine each
session came from. They can move the archive to another computer, browse it,
extract the original files, and explicitly reparse supported sources without
access to the retired computer.

The first delivery is a controlled, offline move into one personal archive. It
must also collect Claude and Codex captures from three other machines. The
receiving computer may itself be one of those sources. A repeat import or an
import through artifact sync must not create another copy of a conversation
already attributed to the same source machine.

Continuous capture, automatic source deletion, team access controls, arbitrary
cross-machine conversation merging, and cloud object placement are outside this
delivery. The future team service uses a separate archive per team. Its raw
retention, garbage collection and disaster rebuild remain under
[issue #1352](https://github.com/kenn-io/agentsview/issues/1352).

## What we know from the code

The source baseline is PR #2036 at `07de734e1d9b629b16a766145294f6e99cc12ace`.
Hosted and artifact identity paths were also compared with main at
`01b962d50e53727cfd17139c0f29edb4d9f5cd07`. These are observations, not claims
that the proposed behavior already exists:

- `internal/db/raw_archive.go` accepts roots from only one device. The import
  descriptor supplies device, root and machine identities manually.
- Artifact import writes `origin~native-session-id`; local raw reparse writes
  the parser's native ID. A synthetic end-to-end test through both production
  paths reproduced two rows for one Claude conversation.
- `rawcheckpoint.ResolveConfiguredRoot` keeps a random root ID against a local
  path in the checkpoint database. Its receipts are also durable state; that
  database cannot simply be discarded while promising chain continuity.
- Local reparse already uses the normal sync engine, including staged large
  Codex parsing. It publishes a full replacement SQLite copy after the
  selected batch succeeds. Startup resync carries archived content forward; it
  does not open the raw vault to reparse it.
- The pinned Docbank commit `72b055a6bdca17cc5dbf280e953174b21fde2fc9` already
  has `Vault.CreateBackup`, `BackupOptions.Prepare`, `ExtraFiles`, and
  `BackupRepository.Restore`. Restore without the original vault was exercised
  in Docbank's own tests. The current AgentsView filesystem recovery format
  does not use these APIs.
- That pin's Kit dependency still buffers backup extras in memory and rejects an
  extra over 4 GiB (`backup/extras.go`). Large raw-object support does not
  cover application files supplied through `ExtraFiles`.

[Hosted raw sync](../../hosted-raw-sync.md) retains originals and authenticated
source generations. [Artifact folder sync](../../artifact-sync.md) exchanges
normalized sessions; it does not carry the original files or curation. Neither
is a replacement for a complete archive recovery point.

## Keep source identity separate from the computer serving the archive

Use the source's existing AgentsView installation ID as its stable identity when
available. Keep its human-readable machine name as an editable label. Record the
label at capture as historical evidence. A rename, a different mount point, or a
new archive host changes neither identity nor original attribution. If a source
never had an installation ID, generate one once in the capture descriptor and
carry that descriptor with every subsequent copy.

There are several existing identities with different jobs. Do not replace them
all with a new universal ID:

| Record                      | Meaning and rule                                                                                                                                                 |
| --------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Source installation ID      | Identifies the original installation, independent of its label.                                                                                                  |
| Artifact origin             | Identifies an artifact publisher. Bind its existing origin explicitly to the source installation; do not regenerate it or adopt it as the collector's publisher. |
| Hosted tenant and device ID | Identify an authenticated sender within a tenant. Store an explicit binding to source attribution; importing that binding does not grant upload authority.       |
| Configured root ID          | Identifies a provider root on that source. Preserve existing checkpoint IDs; a capture path is only its current location.                                        |
| Archive ID                  | Identifies the archive that owns SQLite state. A whole-archive restore preserves it; collecting another source into that archive does not replace it.            |

Store source records and these bindings in `sessions.db`, and export them in the
capture descriptor and complete backup. Namespace root records by source
installation and provider as well as root ID; two machines may have the same
root name. Existing hosted manifests, receipts and checkpoint databases are
retained unchanged as supplemental evidence. This delivery does not accept those
foreign envelopes as local source heads. It constructs local manifests from the
closed native capture, under the receiving archive ID and source installation
ID, using the recorded root IDs. The explicit bindings explain their
relationship to the hosted tenant and device; local receipts never stand in for
hosted acceptance receipts.

The first offline capture may create roots when no raw-sync roots exist. Save
those IDs in its descriptor. A later raw-sync enrollment must explicitly bind
those roots to the uploader; it must not infer identity from a relocated path.
This delivery preserves existing checkpoint state as supplemental evidence. It
does not make checkpoints disposable or implement resumed continuous capture.

Collection into an existing archive preserves that archive's owner and
publisher. The retirement workflow below instead restores the retiring
installation's seed as an unpublished recovery rehearsal. It preserves that
seed's installation ID, archive ID, artifact origin and recorded machine label;
it never adopts the receiving host's existing installation. Disable provider
discovery, artifact publication and remote sync in the rehearsal, including
automatic startup work. At cutover it becomes the retired installation's
replacement, with one active writer. The receiving host's own captured sessions
remain a separate source.

Preserve existing machine aliases. Reuse explicit machine-identity selection for
historical hostname rows whose owner is not recorded; never assign every row to
the seed's installation. List unresolved machine keys and affected session
counts in the migration report. Source binding must not call local ownership
adoption for a foreign installation.

## One stored conversation when the same origin arrives twice

Add a durable mapping from an origin-qualified provider session identity to the
existing SQLite session ID. Both artifact import and raw reparse must resolve
through it before writing. This is new integration work, not reuse of an
existing common resolver.

For Claude and Codex, the key is
`(source installation, provider, parser Session.ID before transport qualification)`.
This includes the parser's Codex prefix and Claude fork suffix.
`SourceSessionID` is separate provenance and grouping metadata: multiple Claude
branches can share it. Keep the artifact wire pair `(origin, NativeSessionID)`
as a transport alias, and the raw source identity plus parsed session ID as a
raw alias. Parent and subagent links use the parser session key too. Do not
derive identity by blindly stripping `~` from stored strings.

Preserve a session ID that already exists in the receiving archive. For a new
foreign session, use `source-installation-id~parser-session-id` and persist the
provider-qualified mapping. This is deterministic across a fresh assembly from
the same seed and captures. Refuse a collision with an existing unrelated row.
Do not rename the seed archive's sessions or message IDs to standardize their
spelling. Relationship rewrites use the same mapping, including when the parent
arrives later. Namespace collisions are errors, never overwrites.

If artifact import arrives first, registration of that publisher's source
binding lets raw reparse attach to its existing row. If raw import arrives
first, the artifact alias resolves to that row. The mapping, source links and
content publication commit together. Artifact wire validation and publisher
authority remain unchanged. The collector does not export foreign sessions as
new conversations authored by itself.

A publisher without a known source binding remains explicitly unattributed.
Neither equal machine labels, equal paths nor equal message text establish the
binding. An operator may bind the publisher using the source descriptor. If two
existing rows would then collapse, report the conflict and preserve both;
automatic reconciliation of previously duplicated rows is outside this slice.

Raw retention and publication are separate. A later artifact update cannot
replace content selected from an accepted raw source merely because it arrived
last. For a raw-backed conversation, retain the artifact evidence and report a
different content revision; explicit raw reparse owns the displayed projection.
Artifact-only conversations keep their existing update behavior. A raw reparse
may update that projection through the normal sync engine, preserving curation
and reporting ambiguous message identity under the existing storage rules.

The same provider ID observed on two different source machines remains two
source observations. This slice does not silently merge their content or
curation. Hosted raw derivation already has separate source and logical-group
identities; retain that distinction. Local observation mappings must not change
the hosted group algorithm or claim to be a global cross-archive session ID.

## Collect closed captures, then reparse deliberately

Keep the current command roles: `archive import` retains originals,
`archive verify` checks retained content, `archive reparse` updates browsable
sessions, and `archive extract` recovers native files. Collection never deletes
the source. Reports distinguish retained, reparsable, supplemental, conflicting,
and failed sources instead of treating successful parsing as proof of backup.

The input is a closed capture with a versioned descriptor, checksummed
inventory, source bindings and root-relative paths. Add descriptor generation to
the capture workflow so users do not invent IDs in handwritten import JSON. The
descriptor records capture time, tool versions, omissions and whether writers
were stopped. Give each immutable capture its inventory digest as its capture
ID, including identity and root bindings in that digest. Reject unsupported
descriptor versions and incomplete inventories. Copies of live SQLite databases
use SQLite backup, not a lone main-file copy. A rolling capture is useful
evidence but is not the final retirement cutoff.

Preserve every selected Claude/Codex original and companion file, including
files outside provider capture plans. Retain other providers' native trees,
historical databases and config as supplemental files even when they cannot be
reparsed. Record excluded special files or symlinks explicitly; no silent skips
and no dereferencing outside the selected roots. Original paths are provenance,
not paths the receiver is allowed to read.

Use the existing provider capture plans, canonical raw manifest validation,
content store and verified materializer. Keep objects before accepting a
manifest, and persist acceptance in SQLite before reporting custody. Keep the
current immutable-source conflict policy: changed bytes for an accepted source
are retained but do not automatically replace its accepted head. A repeated
identical import is a no-op; moving its input directory does not create roots.
Do not route offline files through the upload spool or inherit its 1 GiB budget.

Persist each capture's exact inventory even if source acceptance reports a
conflict. Add `archive extract --capture ID` to select that inventory and write
only its version of each path. Extraction without a selector is permitted only
when every retained path is unambiguous. Recovery of conflicting bytes must not
require accepting them as a source head or guessing a winner by import order.

Only Claude and Codex are initially eligible for raw reparse. Stream them
through the normal local sync engine, with its content policy, stable message
IDs, asset handling, export index and secret scanning. Keep the existing
scratch-database publication boundary. Never use the hosted parser's buffered
result as a shortcut for large local transcripts.

Select archived sources explicitly. Parser-version startup resync carries their
stored content, source mappings, curation and parse status forward unchanged. It
does not select the whole vault for reparse. The offline command owns both
SQLite and the raw vault and refuses to run while the daemon owns them. No new
background queue, lease system or worker-process vault handoff is needed here.

## Preserve curation without turning this into a general database merger

Choose one captured AgentsView database as the seed: the latest verified
snapshot of the installation being retired, taken with its assets and identity
records. Older snapshots remain supplemental evidence. A whole-archive restore
of this seed preserves session IDs, stable message IDs, stars, pins, names,
trash, export identity and every other persistent archive table. Do not seed
from whichever snapshot happens to have the largest file size.

Import the other machines' raw captures into that isolated seed archive using
source bindings. Their captured databases and assets remain recoverable
supplemental evidence. Their curation is **not** automatically merged into the
browsable seed archive in this delivery. Existing peer imports in the seed must
be attributed using recorded publisher bindings before selecting raw reparses;
unresolved collisions are reported. This boundary must be visible in the import
report and retirement checklist, not hidden behind a claim to merge archives.

The rehearsal is disposable and must receive no unique user curation. Synthetic
curation checks run in a separate copy. Keep real changes on the source archive
until its final capture. If someone curates the rehearsal, stop final assembly
and preserve that database; this slice cannot silently merge its edits back.

For the final cutoff, capture the stopped source again and assemble a **fresh**
archive from that final seed and the selected closed captures. Do not refresh
the rehearsal in place: changed inputs cannot advance its immutable heads. Reuse
source descriptors and deterministic foreign IDs; keep earlier captures and
recovery points independently recoverable. This retirement procedure assumes the
source's seed has not itself been used as the rehearsal collector. If it already
has conflicting accepted raw heads, report that blocker; do not clear its raw
ledger or silently promote a different generation.

## Use Docbank's backup format and keep mutable databases local

Replace the unreleased filesystem recovery format in PR #2036 with Docbank's
embedded portable backup API. Pin a merged Docbank revision containing #741 and
validate the resolved Kit dependency; do not assume a merge fixes unrelated
dependency failures. Keep existing experimental recovery points and their saved
readers untouched until the replacement has passed a cold restore.

Before using that layout, require bounded streaming and chunked capture for
`BackupExtraFile`, including files over 4 GiB. The currently pinned extras path
does not meet this requirement. Resolve it in Kit/Docbank using their existing
object recipes, with large-extra coverage for full verification, restore and
prune. Exercise both an application database and an ordinary-vault database.
This is a named prerequisite, not functionality supplied by the existing
large-content change. Do not add gzip workarounds or a second AgentsView backup
format to bypass it.

The first backup remains a stopped-owner operation. Acquire AgentsView's writer
lock, keep both embedded vaults exclusive, and prevent application mutations
until completion. Use `Vault.CreateBackup` on the raw vault. Its `Prepare`
callback creates the consistent application SQLite snapshot. Enumerate that
snapshot, identity and source descriptors, `{dataDir}/assets`, config, and the
closed ordinary artifact vault as `ExtraFiles` under `application/`. The
ordinary artifact vault uses only its local store in this delivery; reject
external store bindings rather than make an incomplete filesystem copy.

Docbank's freeze does not freeze another database or vault. Keep the ordinary
artifact vault closed and immutable for the entire backup under the application
lock. Record exact component paths in one application inventory extra; Docbank's
manifest owns their sizes and hashes. No second pack, compression or checksum
format is needed. Mark captured config with credentials as `Sensitive` and honor
Docbank's explicit plaintext-secret policy; do not silently enable its override.

Restore opens only the backup repository and uses `BackupRepository.Restore` in
a new staging directory. It then assembles the application layout from the
restored raw vault and `application/` extras, verifies SQLite integrity, all
inventory objects, source heads and required assets, and publishes to a new
destination only after the complete check succeeds. A matching vault ID alone
does not prove that a restored copy contains the latest accepted manifests.
Failure leaves the existing destination unchanged and reports staging cleanup.

Restored credentials and provider paths are evidence, not automatically active
configuration. A validation restore starts with a new explicit runtime config,
no live provider scanning, no remote writes and loopback access. Restoring a
historical archive must never initiate uploads with the retired device's token.

The live SQLite databases and vault metadata stay on the archive host's local
disk. Initially a NAS or external drive holds closed Docbank backup repositories
and captures. It is not a shared writable AgentsView data directory. This avoids
depending on network-filesystem WAL behavior; see
[SQLite's WAL constraints](https://sqlite.org/wal.html). Moving primary object
storage to a NAS or cloud backend is separate work and must retain this same
source identity and topology-independent recovery contract.

## Relationship to work already in progress

[PR #1741](https://github.com/kenn-io/agentsview/pull/1741) adds finite
resumable raw backfill;
[PR #1751](https://github.com/kenn-io/agentsview/pull/1751) adds retained-raw
migration parity and extracts shared source/group identity logic. They are
coordination points, not assumed merged prerequisites. Review their current
heads when implementing the source bindings. Reuse their identity semantics
without copying the PostgreSQL acceptance, leasing and publication subsystem
into SQLite.

Offline import and hosted raw upload are inputs to retained raw history. They
can have different receivers and authentication while preserving source
attribution. Artifact exchange stays a normalized view of that history. This
slice adds explicit bindings between those paths; it does not promise that an
offline receipt is a hosted acceptance receipt or that a local collector is
already a hosted raw-sync endpoint.

## Acceptance evidence before retiring the source computer

Use a fresh isolated deployment on the receiving computer and protected copies
from four real source machines. Keep the existing service, binary, databases,
vaults, credentials and provider roots untouched. Record the exact build,
descriptor and inventory hashes, source cutoffs, input counts and commands in a
private runbook. The following are completion criteria, not tests already run:

1. Import all four sources. Browse and filter their sessions by original
   machine, including sessions created on the receiving host. Rename a label
   and relocate a capture; stable identities, accepted heads and counts remain
   unchanged. Repeat import after restart with the same result.
1. Exercise artifact-then-raw and raw-then-artifact for the same known source.
   Each produces one conversation per parser session, with unchanged existing
   IDs, links, stars and pins. Include Claude forks and subagents so a shared
   `SourceSessionID` cannot collapse distinct conversations. Test
   unknown-origin and conflicting-origin cases: evidence is retained,
   ambiguity is reported, and unrelated rows are not overwritten.
1. Compare the seed database's session IDs, message IDs, stored content,
   curation, trash and assets before and after collection, backup, restore and
   full resync. Check the declared limitation on other machines' curation in
   the report. Preserve provider data that raw reparse does not support.
1. Back up, make the source trees and first vault unavailable, then cold-restore
   on the receiver from the repository alone. Extract and hash every captured
   file against its selected capture inventory, including conflicting versions
   and a file over 4 GiB. Include a backup application extra over 4 GiB; large
   raw content alone does not exercise that contract. Reparse selected Claude
   and Codex sessions, a large Codex transcript and a fork with its parent.
   Prove the parser ran; carried-forward SQLite rows alone are not reparse
   proof.
1. Interrupt import and reparse; retry without duplicate acceptance or partial
   publication. A missing blob, corrupt backup extra or identity conflict must
   not damage the previous browsable archive. Startup after a parser
   data-version change must carry archived-only sessions forward without
   scanning the vault.
1. Establish the final stopped-writer cutoff for the retiring computer,
   including work since the first capture. Verify two independent copies on
   different physical storage, at least one off that computer, and a cold
   restore from the final recovery point. Both copies must survive erasing the
   source computer. Assemble this final archive afresh as described above,
   including a session that changed after rehearsal; verify its final accepted
   bytes can be reparsed. Record exactly which sessions and files the cutoff
   covers, remaining exclusions, and the recovery command and reader version.

The old computer is not ready to erase merely because import succeeds or a
backup verifies. The final cutoff, complete extraction comparison, preserved
curation, stable attribution and independent-copy restore are the decision
evidence. Deletion remains an explicit user action. Long-term preservation also
requires retaining readable formats and periodically checking the copies; this
delivery cannot promise permanent storage without that maintenance.
