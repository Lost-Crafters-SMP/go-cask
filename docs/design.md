# Cask: verified filesystem content-addressable storage

**Status:** Proposed v1 design; no implementation.

## 1. Goals and non-goals

Cask stores immutable blob bytes under an explicitly typed cryptographic digest.

V1 provides:

- Streaming ingestion with an expected digest.
- Verification before publication.
- Deduplication within each digest algorithm.
- Atomic, non-overwriting publication.
- Concurrent use by goroutines and cooperating processes.
- Explicit corruption verification.
- Deterministic filesystem layout.

It does not provide downloading, application metadata, aliases, references,
reachability, GC policy, remote storage, compression, encryption, or mutable
key/value storage.

## 2. Design alternatives and recommendations

| Choice | Recommendation | Reason |
|---|---|---|
| Expected-digest-only versus compute-and-return ingestion | Expected-digest-only `Put` | Makes verification against caller expectations mandatory. Defer a separately named compute-and-store operation. |
| Interface-first versus concrete store | Concrete `*Store` | V1 implements only filesystem storage. Consumers can define narrow interfaces themselves. |
| Verify existing objects versus trust publication | Always verify existing objects before `Put` succeeds | Detects corruption and avoids trusting hash-shaped filenames. |
| One algorithm versus typed multiple algorithms | SHA-256 and SHA-512, explicitly typed | Small approved set without implicit identity or algorithm extensibility. |
| Removal in v1 versus defer | Defer | Keeps lifetime and Windows open-file deletion races outside the initial contract. |
| Enumeration in v1 versus defer | Defer | Correct ingestion and reads come first; enumeration needs its own invalid-entry contract. |
| Per-digest locks versus atomic convergence | Atomic no-clobber publication | Avoids cross-process lock lifecycle and crash recovery. Locks are unnecessary for correctness. |
| Hard links versus platform-specific exclusive rename | Internal platform-specific no-clobber publication; hard links as Unix fallback | Keep the public contract semantic without requiring a filesystem feature unnecessarily. |
| Mandatory staged-file reread versus single-pass ingestion | Single-pass ingestion; explicit `Verify` remains available | Checked writes and finalization cover ordinary ingestion failures; mandatory rereading adds predictable linear work for limited extra coverage. |

**Important consequence:** “Portable” means correct behavior on supported local
filesystems, not guaranteed operation on every filesystem. V1 requires filesystem
support for atomic no-clobber publication of a completed staged object.
Hard links are an internal fallback, not a public requirement. Unsupported
environments fail explicitly; publication never falls back to a weaker protocol.

## 3. Public types and digest model

### Algorithms

`Algorithm` is a closed enum with two valid values:

- `SHA256`
- `SHA512`

The zero value and unknown values are invalid. There is no algorithm registration
mechanism.

SHA-256 is the recommended default for callers, but the store has no implicit
algorithm.

### Digests

`Digest` contains an algorithm and fixed-size digest bytes in unexported fields.
It is comparable with `==`; its zero value is invalid.

Canonical textual forms:

```text
sha256:<64 lowercase hexadecimal characters>
sha512:<128 lowercase hexadecimal characters>
```

Parsing:

- Requires an explicit algorithm.
- Accepts only approved, lowercase algorithm names.
- Requires the exact digest length.
- Accepts uppercase hexadecimal input but normalizes it to lowercase.
- Rejects whitespace, abbreviated values, prefixes such as `0x`, and additional
  separators.

Unknown algorithms produce `ErrUnsupportedAlgorithm`; malformed syntax or
incorrect values produce `ErrInvalidDigest`.

`String` returns canonical text for valid values. Its invalid-zero-value
representation is diagnostic, not parseable. `MarshalText` rejects invalid
values; `UnmarshalText` changes its receiver only after successful parsing.

`NewDigest` accepts algorithm plus raw digest bytes and copies those bytes.
`Bytes` returns a copy.

No public ordering API is needed. Canonical strings provide a deterministic
ordering if callers need one.

### Independent keys

Each algorithm digest is an independent key.

Identical bytes stored under SHA-256 and SHA-512 occupy separate objects. There
is no alias database or automatic cross-algorithm deduplication.

Multiple verified digests may be tracked by a higher layer, but Cask does not
choose a canonical storage key for them.

### Intrinsic information

`Info` contains only:

- `Digest`
- `Size`, in bytes

It contains no filenames, timestamps, acquisition sources, ownership, or
application relationships.

An `Info` value does not itself prove current integrity: its provenance matters.
`Put` and `Verify` return verified information; `Open` returns observed information
without hashing.

## 4. Small public API

V1 exposes:

- `New`: create/open a filesystem store.
- `Put`: ingest and verify bytes against an expected digest.
- `Open`: obtain a streaming reader without rehashing.
- `Verify`: explicitly rehash a stored object.
- `Close`: release store-owned directory handles.

There is no `Has`, `Stat`, `Path`, `Remove`, or `Walk` initially.

`Open` already reports missing objects and size. A separate `Has` would introduce
an easily misinterpreted existence-versus-integrity distinction.

The package is simply:

```text
go.lostcrafters.com/cask
```

No `fsstore` subpackage or backend interface is needed yet.

## 5. Integrity guarantees

For every successful `Put(ctx, expected, reader)`:

1. The supplied stream reached EOF without an ingestion error.
2. The digest of the complete byte sequence successfully written during ingestion
   matched `expected`; every write was checked for errors and short writes.
3. The final object was established by either:
   - Publishing that completed temporary file after successful sync and close, or
   - Verifying an already-existing final object.
4. The returned size is the complete ingestion size for a newly published object,
   or the measured size of the verified existing object.

New ingestion is single-pass: Cask hashes the bytes it writes, checks all writes,
and propagates finalization failures. Assuming the filesystem faithfully stores
successful writes, the published bytes match the expected digest.

`Put` does not independently reread a newly staged or newly published object.
Silent changes not surfaced by write/sync/close errors require a later `Verify`
or an existing-object check to detect. Neither single-pass ingestion nor a reread
proves physical-media persistence independently of the OS and hardware.

Normal `Put` never overwrites an existing object.

Additional guarantees:

- Input mismatch never causes publication by that writer.
- Incomplete temporary files never become final objects.
- Readers never observe a partially ingested object through its final path.
- A competing writer may publish the same digest even while another writer fails.

These guarantees assume approved algorithms, correct OS primitives, and no
external modification of objects.

## 6. Threat model

Cask protects against:

- Supplied bytes differing from the expected digest.
- Interrupted ingestion.
- Partial writes being mistaken for complete objects.
- Accidental corruption detected during `Put` deduplication or `Verify`.
- Cooperating concurrent writers replacing one another.

It does not protect against:

- A malicious process with write access to the store.
- A compromised digest algorithm.
- An incorrect expected digest chosen by the application.
- The OS or storage hardware lying about writes or reads.
- Corruption introduced after verification and before or during later reads.

**Content identity is not authenticity.** Digest verification does not identify
a publisher or establish whether content is trustworthy.

## 7. Put lifecycle

```text
validate digest, options, and context
              |
prepare confined shard directory
              |
create exclusive random temporary file
              |
stream input -> temporary file + input hash
              |
EOF, size-limit check, expected-digest comparison
              |
sync temporary file; close writable handle
              |
check context immediately before publication
              |
atomic no-clobber publication: temporary path -> final path
       /              |                  \
  created       destination exists      other error
     |                 |                    |
     |          verify final object         |
     |          /               \           |
     |        valid            corrupt       |
     |          |                 |          |
clean up temporary name if still present    return corruption/error
     |
return verified Info
```

Temporary cleanup occurs on every completed exit path, best effort on failure.
Cleanup errors are not silently discarded. Successful rename consumes the
temporary name; hard-link fallback leaves it for cleanup. A losing publisher
retains its staged file until cleanup.

### Duplicate ingestion

V1 consumes and verifies the supplied reader even when the object already
exists. This provides one consistent contract: successful `Put` verifies both
the supplied bytes and the stored object.

Therefore duplicate `Put` is not O(1). It performs ingestion and existing-object
verification.

Callers that do not need to supply bytes again can use `Verify`. Callers willing
to read without an integrity check can use `Open`.

No trust-existing option or cached verification metadata is included initially.

### Verification failures

- Supplied stream differs from expected: `ErrDigestMismatch`.
- Existing final object differs: `ErrCorruptObject`.

No automatic repair occurs.

### Staged-file reread decision

| Model | Guarantee and coverage | Cost for a new N-byte blob | API impact |
|---|---|---|---|
| A. Mandatory reread | Independently hashes bytes observable from the staged file before publication; can detect silent mutation or corruption visible to that read | N input read + N filesystem write + N additional filesystem read, plus a second hash | No extra option, but every new `Put` pays the cost |
| B. Optional verify-after-write | Same extra check as A when enabled; two ingestion integrity levels | Same as A when enabled, otherwise C | Adds configuration and another contract/test branch |
| C. Single-pass verified ingestion | Hashes the successfully written stream; checks write, sync, and close results; assumes faithful successful filesystem writes | N input read + N filesystem write, one hash | No public change; explicit `Verify` already exists |

Choose C. Do not add a paranoid option in v1. Applications may call `Verify`
after `Put` to request another read, with the existing non-transactional
verification semantics. That call does not retroactively prevent publication.

The precise error-detection differences are:

| Failure class | Checked ingestion/finalization | Extra staged-file reread |
|---|---|---|
| Caller supplies wrong bytes | Expected-digest comparison rejects them | Redundant |
| Short or partial write | Checked write count/error rejects it; interrupted ingestion cannot publish | Redundant unless the filesystem falsely reports success |
| Buffered write failure | V1 needs no additional user-space buffering; any introduced buffer must flush successfully before sync | Not a substitute for checking flush errors |
| Close or sync failure | Propagated; no publication | Redundant |
| Accidental temporary-file mutation | Not detected if it silently occurs after the bytes were hashed | Can detect a change visible during reread, but not a change after reread |
| Silent filesystem or cache corruption | Later `Verify` or existing-object verification detects observable digest differences | Can detect differences returned by this read, but may read the same cache and miss on-media corruption |
| Hardware lying about persisted data | Outside the guarantee | Does not reliably detect it or prove persistence |

The reread adds N bytes of logical filesystem reads and a second full hash. It
may hit the page cache rather than cause N physical disk reads, but large blobs
can require that additional disk traffic and cache pressure. Its useful extra
coverage is silent, already-observable staging mutation/corruption, not ordinary
reported write failures. That limited coverage does not justify making the
linear amplification mandatory for a general-purpose large-blob store.

Detectable stored corruption remains in scope through explicit `Verify` and
mandatory existing-object verification. Cask does not guarantee detection of
every silent corruption before publication or integrity indefinitely afterward.

## 8. Open and Verify lifecycles

### Open

```text
validate digest and context
           |
open confined final object
           |
confirm regular-file type
           |
inspect size on the opened handle
           |
return io.ReadCloser + Info
```

`Open` does **not** hash bytes. It trusts the publication protocol while
acknowledging that subsequent external corruption is possible.

`Info.Size` comes from the opened handle, not a separate path lookup.

The reader:

- Is owned and closed by the caller.
- Checks context cancellation between reads.
- Does not promise seekability.
- Does not expose internal paths.
- May return bytes before cancellation or an I/O failure becomes observable.

Cancellation cannot necessarily interrupt a blocked OS read.

### Verify

```text
validate digest and context
           |
open final object; reject non-regular entries
           |
stream hash to EOF; count bytes
           |
compare with requested digest
           |
close handle, checking close errors
           |
return verified Info or error
```

`Verify` authenticates the bytes read from that handle during the operation. It
is not a permanent integrity certificate.

`Verify` followed by `Open` is not an atomic verified-read operation. A future
verified-reader API would require separate semantics, particularly because
streaming readers can receive data before final digest validation.

## 9. Deterministic filesystem layout

For digest hexadecimal value `H`:

```text
<root>/objects/<algorithm>/<H[0:2]>/<H[2:4]>/<H[4:]>
```

Example:

```text
<root>/objects/sha256/ab/cd/<remaining 60 characters>
```

Temporary files reside in the same leaf directory:

```text
<root>/objects/sha256/ab/cd/.tmp-<random identifier>
```

Properties:

- Two directory levels bound per-directory fanout.
- Directories are created lazily.
- Algorithms are isolated.
- All generated components use lowercase ASCII.
- Digest-derived names cannot contain separators or traversal components.
- SHA-256 and SHA-512 have predictable filename lengths.
- Temporary names cannot be mistaken for final digest names.

Path construction accepts only validated `Digest` values, never caller-supplied
path fragments.

There are no sidecar integrity records. Digests and actual bytes are authoritative.

The layout is documented for diagnostics, but direct manipulation is
unsupported. Future layout changes require explicit compatibility handling,
not silent reinterpretation.

## 10. Atomic publication and durability

### Why not ordinary rename?

Ordinary `os.Rename` is not a portable no-clobber primitive:

- Unix commonly replaces an existing destination.
- Windows behavior differs.
- Checking destination existence before renaming creates a race.

### Internal publication abstraction

V1 uses a private platform-specific publication helper, not a public backend or
filesystem interface. It accepts a completed staged object and a final basename
in the same confined shard directory. Its outcomes are:

- Published: the final name atomically refers to the complete staged object.
- Destination exists: nothing was replaced; retain the staged file and verify
  the existing final object before `Put` succeeds.
- Failure: preserve the underlying operational error or a distinguishable
  unsupported-operation error; clean up unpublished staging state.

The helper also tracks internally whether successful publication consumed the
temporary name. It must never enable replacement, exchange, or copy fallback.

### Platform comparison and chosen primitives

| Platform | Preferred primitive | Go exposure | Existing destination / losing race | Filesystem boundary | Fallback |
|---|---|---|---|---|---|
| Linux | `renameat2(..., RENAME_NOREPLACE)` | Not in `os`; `golang.org/x/sys/unix.Renameat2` | `EEXIST`; destination unchanged, staged source retained | Same mounted filesystem; cross-mount operation fails with `EXDEV` | Atomic hard-link creation on distinguishable unsupported exclusive-rename results |
| Windows | `NtSetInformationFile(FileRenameInformation)` with `ReplaceIfExists = FALSE` | Not exposed as a no-replace operation by `os`; small `x/sys/windows` helper | Collision fails without replacement; staged source retained | Same volume; no copy/move-across-volume fallback | None required in v1; unsupported rename is explicit |
| macOS | `renameatx_np(..., RENAME_EXCL)` | Not in `os`; `golang.org/x/sys/unix.RenameatxNp` | `EEXIST`; destination unchanged, staged source retained | Same filesystem; otherwise `EXDEV` | Atomic hard-link creation when exclusive rename is distinguishably unsupported |

Small build-tagged native helpers are justified: they enforce required semantics
that standard `os.Rename`/`Root.Rename` do not promise. No public API changes are
needed. A future implementation may add `x/sys` as an internal dependency.

#### Linux

`renameat2` exists since Linux 3.15, but `RENAME_NOREPLACE` also needs filesystem
support. Support arrived in ext4 in 3.15, btrfs/tmpfs in 3.17, XFS in 4.0, and
many additional filesystems in 4.9. These examples are compatibility context,
not an allowlist. Older kernels, filesystem drivers, or syscall restrictions
can still prevent use.

Use directory file descriptors and generated basenames. With the no-replace
flag, the kernel performs the existence test and rename as one operation; it
cannot replace the destination. Successful rename consumes the staged name.

On `ENOSYS`, `EOPNOTSUPP`/`ENOTSUP`, or `EINVAL` attributable to unsupported flags
with known-valid arguments, try atomic hard-link publication. Do not infer
support failure from generic `EPERM`, `EACCES`, `EIO`, or `EXDEV`. Seccomp can
return `EPERM`; that remains an operational/policy failure, not evidence that
the filesystem lacks support.

Hard-link fallback has the same publication semantics but leaves two names for
the same complete file until temporary cleanup. It requires same-filesystem
hard-link support and can fail for permissions, link limits, or space exhaustion.

#### Windows

Prefer handle-based native `FileRenameInformation` with `ReplaceIfExists = FALSE`. Open the
completed staged file with the access required for rename (`DELETE`), bind the
destination to the confined shard directory, and use the generated final name.
This is a rename request, not a copy or replacement request. No extended
replacement/POSIX-replacement flags are enabled.

Implementation validation on Windows 10.0.26200 confirmed that the originally
proposed Win32 `SetFileInformationByHandle(FileRenameInfo)` returned
`ERROR_INVALID_PARAMETER` with a non-null directory handle and relative target;
an absolute-path control succeeded. The approved correction uses
`NtSetInformationFile(FileRenameInformation)`: the confined relative-target
probe succeeded and consumed staging; a collision returned
`STATUS_OBJECT_NAME_COLLISION`, retained staging, and left the winner unchanged.
This changes only the private API used, not the public publication contract.

`MoveFileExW` without `MOVEFILE_REPLACE_EXISTING` or `MOVEFILE_COPY_ALLOWED` is
also a same-volume no-clobber move candidate. Handle-based rename is preferred
because it identifies the source by handle and can bind a relative destination
to a directory handle. `os.Rename` is unsuitable: it has no portable no-replace
contract, and Windows implementations may request replacement.

Rename-related handle sharing matters. Close the ingestion handle after sync;
the private publisher may reopen a rename-capable handle and must close it with
errors propagated. Its sharing modes must not unnecessarily block readers.
Other processes that opened the staged file without delete sharing, including
antivirus/indexers, can prevent rename or cleanup. Sharing violations and access
denial are operational failures; do not weaken the protocol or classify them as
unsupported filesystems. No automatic retry policy is added in v1.

Normalize documented collision results such as `ERROR_FILE_EXISTS` and
`ERROR_ALREADY_EXISTS` to destination-exists. Do not treat every
`ERROR_ACCESS_DENIED` or `ERROR_SHARING_VIOLATION` as a collision. Under
interference, a writer may fail rather than converge successfully, but it must
not overwrite or partially publish. Cross-volume and path failures retain their
causes; copying is never enabled.

#### macOS

Apple's exclusive rename facility, `renameatx_np` with `RENAME_EXCL`, atomically
fails if the destination exists on filesystems supporting
`VOL_CAP_INT_RENAME_EXCL`. Prefer it on supporting local filesystems, including
normal modern APFS deployments: it consumes staging in one operation and avoids
a redundant temporary hard link. Do not assume every mounted filesystem or OS
configuration supports it. `renamex_np` offers the path-based equivalent;
directory-relative `renameatx_np` better fits confined publication.

Use the `x/sys/unix` wrapper rather than hand-written raw Darwin syscall numbers.
On identifiable unavailable/unsupported operation results, use hard-link
publication if supported. Preserve unrelated permission, path, and I/O errors.
Neither strategy replaces an existing final object.

### Rejected strategies

Exclusive creation of the final path followed by copying/writing is rejected.
`O_EXCL` prevents replacement, but exposes an empty or partial final file and
can leave it behind after a crash. Reader locks or a separate completion marker
would change the existing protocol and are not introduced.

Ordinary rename after an existence check, remove-then-rename, cross-volume
copy-and-delete, and replacement/exchange flags are also rejected. Atomic
creation alone is insufficient: visibility must publish an already-complete
object in one namespace operation.

There is no copy-to-final fallback and no remove-then-rename fallback. Either
would weaken the contract.

### Support detection and errors

Attempt the selected primitive on the real staged object; do not use broad
filesystem-name allowlists. Platform guarantees establish semantics, while
return values establish whether this invocation is supported. A successful
single probe is not proof that an arbitrary driver implements atomicity correctly.

Fallback occurs only on identifiable unavailability/support failures with
validated arguments. Ambiguous `EINVAL` remains an error unless its cause can
be established; `ERROR_INVALID_PARAMETER` likewise is not automatically a
filesystem-support result. On macOS, `ENOTSUP` is an explicit unsupported-flags
result; capability information may corroborate it without replacing the actual
operation.

Return `ErrUnsupportedFilesystem` only if no selected correct strategy is
available and that support failure is distinguishable. Preserve permission,
sharing, I/O, path, quota, and storage-exhaustion errors, including failures from
a fallback attempt. A kernel/API unavailability may contribute to this sentinel
even when the limitation is not literally the disk format. Never hide an
operational failure behind it.

### Atomic visibility

On supported local filesystems:

- Final publication is indivisible.
- The destination is absent or refers to the completed file.
- A preexisting final object is never replaced.

### Persistence

V1 calls `File.Sync` on the completed temporary file before publication and
propagates sync failures.

It does **not** promise namespace persistence across sudden power loss. Portable
Go does not provide identical directory-sync guarantees across Unix and
Windows, and v1 does not expose a stronger durability mode.

Thus:

> Successful `Put` means verified publication, not guaranteed survival of sudden
> power loss.

After a crash, the object may be present or absent. When present through a
correctly implemented filesystem, it was published only after completion and
verification.

Directory syncing and a precisely supported durable mode can be considered later.

### Process-crash states

- Before publication: only an incomplete or complete temporary file may remain.
- After successful exclusive rename on Linux/macOS or handle rename on Windows:
  the final object is complete and the temporary name has been consumed.
- After successful hard-link fallback: the final object is complete; the
  temporary name may also remain if cleanup did not run.
- After a losing collision: the winner's complete object remains and the loser's
  temporary file may remain.

A process crash cannot turn any selected primitive into a partial copy at the
final path. These namespace states are not a sudden-power-loss persistence
guarantee; file sync and directory durability remain distinct.

### Failure after publication

Temporary-name removal after hard-link fallback, or closing a publication
handle, can fail after the final object becomes visible. Cask reports those
failures. Successful exclusive rename needs no temporary-name removal.

Consequently, a `Put` error does not universally imply that no object exists.
Retrying `Put` or calling `Verify` is safe.

This avoids a bespoke transactional error hierarchy and must be documented
prominently.

## 11. Concurrency model

Methods are safe for concurrent goroutines, except `Close` must not race with
store method calls.

Across cooperating processes:

1. Writers create distinct temporary files.
2. Each hashes its successfully written stream and finishes sync and close.
3. Exactly one writer atomically publishes to the absent final name, using
   exclusive rename or hard-link fallback.
4. A losing writer receives destination-exists, retains its staged file, and
   verifies the winner’s final object before cleaning up.
5. All successful writers converge on that object.

Every selected strategy combines the destination check with publication. Even
writers using different correct strategies cannot replace an occupied name.
Both writers may succeed when the winner's object verifies; operational errors
can still cause either writer to fail. No user-space check-then-act lock is
needed for correctness.

Destination-exists races are handled internally, not exposed as semantic errors.

No global or per-digest lock is required. Same-process per-digest locking could
reduce duplicate work later, but must not become necessary for correctness.

Concurrent reads see either:

- Not found before publication.
- The complete object after publication.

V1 excludes concurrent external deletion, replacement, or repair from its
cooperative-writer guarantees.

## 12. Immutability and corruption behavior

Cask never opens published objects for writing.

For an existing digest path:

- Matching bytes: deduplication succeeds.
- Different bytes: report corruption.
- Symlink or non-regular entry: reject it.
- Unreadable object: report the underlying I/O error, not an unproven corruption
  claim.

A good supplied stream does not repair a corrupt stored object.

V1 has no repair API. Repair requires independently designed coordination with
readers and writers, plus clear replacement semantics.

Filesystem permissions can discourage modification but are not the integrity
boundary. Read-only attributes are not required; in particular, they complicate
Windows cleanup and administration.

## 13. Errors

Use a small sentinel set:

- `ErrInvalidDigest`
- `ErrUnsupportedAlgorithm`
- `ErrDigestMismatch`
- `ErrCorruptObject`
- `ErrTooLarge`
- `ErrUnsupportedFilesystem`

Use standard identities for other conditions:

- `fs.ErrNotExist`
- `fs.ErrPermission`
- `context.Canceled`
- `context.DeadlineExceeded`

Preserve OS errors for storage exhaustion and other I/O failures. There is no
portable bespoke “disk full” abstraction in v1.

A `DigestMismatchError` exposes expected and actual digests and unwraps to
`ErrDigestMismatch`.

A `CorruptionError` exposes requested and observed digests and unwraps to
`ErrCorruptObject`.

Operation context is added using wrapping. Multiple primary/finalization errors
can be preserved using `errors.Join`.

Publication failures are not all classified as unsupported filesystems:
permission and operational failures retain their real causes.
`ErrUnsupportedFilesystem` is used only when support failure is identifiable.

## 14. Context and resource safety

`Put`, `Open`, and `Verify` accept contexts. Pure digest methods and `Close` do not.

Cancellation:

- Is checked during streaming and before publication.
- Aborts unpublished work.
- Attempts temporary cleanup.
- Cannot forcibly interrupt an arbitrary blocked `io.Reader`.
- Can race with the publication commit point.

Once publication succeeds, Cask completes finalization rather than reporting
cancellation alone. A successfully committed operation may therefore return
success even if cancellation arrived concurrently.

### Size limit

`Options.MaxBlobSize` is a nonnegative `int64`:

- Zero: unlimited.
- Positive: maximum accepted ingestion size.
- Negative: constructor error.

The implementation detects excess input without unbounded buffering or writing
beyond the configured limit. Size accounting checks overflow.

The limit is an ingestion safeguard, not a storage quota or eviction policy.

### Ownership and failures

- Cask does not close the supplied reader.
- Cask owns all temporary and verification handles.
- The caller owns the reader returned by `Open`.
- Short writes, read failures, sync failures, and close failures are propagated.
- A reader returning bytes and an error has those bytes accounted for; a non-EOF
  error still fails ingestion.
- Handles are closed before Windows-sensitive unlink operations.

`Close` releases the store’s root handle, is idempotent, and does not close
readers already returned to callers. Those readers remain caller-owned.

## 15. Path safety and platform assumptions

### Root confinement

Use Go’s `os.Root` for root-relative filesystem operations. Native publication
needs a small private, handle-relative bridge because `Root` has no public
exclusive-rename method or exported native directory handle.

Acquire a pinned shard-directory file/handle through confined root operations;
pass only generated single-component names to Unix directory-relative rename
or link calls. On Windows, acquire the rename-capable source handle through a
confined directory-relative native open and bind the target to that same pinned
directory. Do not reconstruct absolute paths from `Root.Name()` and thereby
discard confinement. Keep native handle acquisition, ownership, and closure
inside the private publisher; no platform handles enter the public API.

`New` requires an existing root directory and initializes internal directories.
Resolving and opening that initial root is a trusted setup operation: the caller
controls its location and ancestors.

Root confinement protects against ordinary path traversal and links escaping
the opened root on supported platforms.

### Symlinks and unusual entries

Cask rejects observed symlinks at object paths and internal layout directories
using root-relative inspection. Existing objects must be regular files.

However, Go’s root operations can follow permitted in-root symlinks. Inspection
alone is not a complete no-follow guarantee against concurrent hostile replacement.

Therefore:

- The store directory must not be concurrently manipulated by an adversary.
- Rejecting symlinks is defense in depth.
- Cask does not claim universal protection against all junctions, reparse points,
  bind mounts, device files, or hostile filesystem races.

Internal shard directories must remain on the same filesystem as their temporary
and final files. Administrative mounts inside the layout are unsupported.

### Windows

Test specifically:

- Handle-based no-replace rename on local supported filesystems, initially NTFS.
- Destination-exists error classification.
- Closed-handle cleanup.
- Junction/reparse-point confinement behavior.
- Path-length failures.
- Sharing violations and interference from antivirus/indexing tools.

Lack of hard links alone does not exclude a filesystem, including FAT/exFAT.
Such a filesystem is supported only if its native no-clobber rename satisfies
the contract; support is not presumed solely from its name. Operational sharing
and access failures remain distinct from unsupported publication.

Linux and macOS likewise require a local filesystem providing either supported
exclusive rename or the hard-link fallback. Network filesystems remain outside
the initial guarantee.

## 16. Temporary files and crash recovery

Ordinary failures trigger cleanup. Process termination can leave temporary files
behind.

V1:

- Never interprets temporary files as objects.
- Never publishes them automatically after restart.
- Does not delete arbitrary stale temporary files during startup.

Age-based cleanup can accidentally delete another process’s active ingestion,
so no automatic expiry policy is introduced.

Administrators may remove temporary files while all writers are stopped. A
coordinated maintenance API can be designed separately.

## 17. Enumeration, removal, and policy boundary

Enumeration and removal are deferred.

Future primitives may provide:

- Deterministically ordered object enumeration.
- Observed sizes without implicit verification.
- Explicit local deletion.

They must distinguish valid names from verified content, report malformed
entries, and define Windows open-reader behavior.

They must not decide LRU, expiry, quotas, pinning, ownership, reference counts,
or application reachability.

V1 contains no GC or storage-policy layer.

## 18. Deterministic offline test matrix

Use temporary directories and real filesystem operations. Use barrier-controlled
readers rather than sleeps for concurrency.

| Area | Required cases |
|---|---|
| Digests | SHA-256/SHA-512; canonical text; uppercase hex normalization; invalid zero value; malformed lengths; unsupported and weak algorithms; traversal-shaped input |
| Round trips | Small, empty, and large generated streams; returned digest and size |
| Ingestion failures | Mismatch; reader failure after partial output; cancellation; data plus non-EOF error; size limit and boundary |
| Ingestion verification | Hash the full successfully written stream; no mandatory staging reread; write/sync/close failures prevent publication; separately injected silent mutation is detectable by explicit `Verify`, not promised to be rejected before publication |
| Deduplication | Repeated `Put`; existing valid object; existing corrupt object; supplied bad stream when a valid object already exists |
| Publication | Concurrent same-digest writers; distinct-digest writers; existing valid/corrupt destination; forced destination-exists race; no overwrite; readers see absence or complete bytes; losing source retained until cleanup |
| Immutability | Corrupt existing object remains unchanged after failed `Put` |
| Verification | Success; corruption; missing object; cancellation |
| Reads | Missing object; size from opened handle; unverified corrupted bytes remain observable through documented `Open` semantics; caller close |
| Layout | Exact deterministic path; algorithm isolation; shard fanout; ignored temporary names |
| Cleanup | Reader/mismatch/cancellation failures; cleanup failure after publication |
| Filesystem safety | Object symlink; symlinked shard; outside-root link attempts; non-regular entries |
| Operational failures | Permission denial; write failure; short write; sync, close, rename/link, and cleanup failures; operational errors never mislabeled unsupported |
| Processes | Child-process duplicate publication with coordinated barriers |
| Crashes | Process termination before publication, immediately after rename, and after hard-link fallback before cleanup; final path never contains partial bytes |
| Linux/macOS publication | Native exclusive rename success/collision; unsupported-operation fallback to hard link; no available strategy; valid-argument error classification; mixed-strategy process convergence |
| Windows | No-replace handle rename success/collision; delete-sharing interference and access denial; publication-handle closure; junctions; unsupported-operation classification; no cross-volume copy fallback |
| Lifecycle | Concurrent method use; idempotent close; previously returned readers survive store close |

Permission tests must skip environments where elevated privileges invalidate
the scenario.

Use a narrow, unexported fault-injection seam only for failures impractical to
cause deterministically with real filesystems. Do not create a public filesystem
abstraction for testing.

Run the suite with the race detector where supported. No network is required.

## 19. Benchmarks

Measure:

- New `Put`: generated 1 MiB and 100 MiB streams.
- Existing-object `Put`, including its documented verification cost.
- `Open` handle acquisition separately from reading.
- `Verify` throughput.
- Concurrent duplicate publication.

Report allocations and throughput. Do not add caching, locks, aliases, or
durability shortcuts solely to improve benchmark results.

## 20. API stability and unresolved questions

Stable semantics should include:

- Canonical digest encoding and approved algorithms.
- Independent algorithm keys.
- Expected-digest verification.
- Non-overwriting publication.
- Unverified `Open` versus verified `Verify`.
- Error identities.
- Resource ownership and cancellation behavior.
- Intrinsic metadata only.

Temporary names, copy buffer sizes, internal synchronization, and fault-injection
mechanisms remain implementation details.

Before implementation, confirm:

1. **Native publication validation:** Confirm handle-relative integration and
   collision/unsupported-error mappings in the selected Go/x/sys versions and
   platform tests. Exclusive rename is preferred; Unix hard links are fallback,
   not a public filesystem requirement.
2. **Verification policy:** Single-pass verified ingestion is recommended and
   mandatory staged reread is removed. Confirm acceptance of the explicit
   faithful-write assumption; callers can request a separate `Verify` without
   adding a store option or per-call mode.
3. **Durability:** Is atomic visibility plus file sync sufficient? A stronger
   durability mode needs platform-specific directory persistence semantics.
4. **Go baseline:** Keep the scaffold’s Go version, which supplies the required
   `os.Root` operations.
5. **Scope:** Confirm deferral of computed ingestion, verified streaming reads,
   removal, enumeration, repair, and automatic temporary cleanup.

## 21. Recommended v1 scope

Ship one concrete filesystem store, two approved digest algorithms,
expected-digest streaming ingestion, mandatory verification of existing objects,
unverified streaming reads, explicit verification, and atomic no-clobber
publication.

New ingestion hashes once, checks all writes and finalization, and does not
reread staging. Publication uses private platform-specific exclusive rename
with a correct hard-link fallback on selected Unix environments. There are no
public publication flags, backend interfaces, or paranoid-mode options.

Prefer a clear unsupported-filesystem error over a weaker fallback.

### Recommended API sketch

```go
type Algorithm uint8

const (
    SHA256 Algorithm = 1
    SHA512 Algorithm = 2
)

func (a Algorithm) String() string

type Digest struct {
    // Unexported comparable representation.
}

func NewDigest(algorithm Algorithm, sum []byte) (Digest, error)
func ParseDigest(text string) (Digest, error)

func (d Digest) Algorithm() Algorithm
func (d Digest) Bytes() []byte
func (d Digest) String() string
func (d Digest) MarshalText() ([]byte, error)
func (d *Digest) UnmarshalText(text []byte) error

type Info struct {
    Digest Digest
    Size   int64
}

type Options struct {
    MaxBlobSize int64
}

type Store struct {
    // Unexported filesystem state.
}

func New(root string, options Options) (*Store, error)

func (s *Store) Put(
    ctx context.Context,
    expected Digest,
    src io.Reader,
) (Info, error)

func (s *Store) Open(
    ctx context.Context,
    digest Digest,
) (io.ReadCloser, Info, error)

func (s *Store) Verify(
    ctx context.Context,
    digest Digest,
) (Info, error)

func (s *Store) Close() error

var (
    ErrInvalidDigest         error
    ErrUnsupportedAlgorithm  error
    ErrDigestMismatch        error
    ErrCorruptObject         error
    ErrTooLarge              error
    ErrUnsupportedFilesystem error
)

type DigestMismatchError struct {
    Expected Digest
    Actual   Digest
}

func (e *DigestMismatchError) Error() string
func (e *DigestMismatchError) Unwrap() error

type CorruptionError struct {
    Expected Digest
    Actual   Digest
}

func (e *CorruptionError) Error() string
func (e *CorruptionError) Unwrap() error
```

## 22. Focused amendment decisions and sources

The public API sketch is unchanged. Only publication mechanics and staged-file
rereading, plus their directly dependent guarantees/tests, are amended.

### Sources

- [Linux rename/renameat2 manual](https://man7.org/linux/man-pages/man2/rename.2.html):
  no-replace semantics, kernel/filesystem availability, and errors.
- [Apple rename manual source](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/man/man2/rename.2):
  `renameatx_np`, `RENAME_EXCL`, volume capabilities, and unsupported results.
- [Go x/sys Darwin wrappers](https://github.com/golang/sys/blob/master/unix/syscall_darwin.go):
  `RenameatxNp` and `RenamexNp` exposure.
- [Windows NtSetInformationFile](https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/ntifs/nf-ntifs-ntsetinformationfile)
  and [FILE_RENAME_INFORMATION](https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/ntifs/ns-ntifs-_file_rename_information):
  native directory-relative rename and replacement-disabled behavior.
- [Windows MoveFileExW](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-movefileexw):
  comparison with move flags, cross-volume copying, and permission requirements.

### Decision 1 — Publication

- **Chosen semantic contract:** Atomically publish an already-complete staged
  object only if the final name is absent; never replace an existing object.
  A collision preserves staging and requires verification of the final object.
- **Chosen Linux implementation:** Directory-relative `renameat2` with
  `RENAME_NOREPLACE`; atomic hard-link fallback on identifiable lack of support.
- **Chosen Windows implementation:** Confined handle-based
  `NtSetInformationFile(FileRenameInformation)` with `ReplaceIfExists = FALSE`;
  same volume, no replacement or copy fallback.
- **Chosen macOS implementation:** Directory-relative `renameatx_np` with
  `RENAME_EXCL`; atomic hard-link fallback on identifiable lack of support.
- **Hard links are:** A selected Unix fallback, not a public requirement.
- **Unsupported-filesystem behavior:** Fail explicitly when no correct selected
  primitive is available and unsupported behavior is distinguishable. Preserve
  operational errors; never downgrade atomicity or no-clobber semantics.

### Decision 2 — Post-write verification

- **Choice:** Mandatory reread removed from default `Put`; no optional mode in
  v1. Explicit `Verify` remains available.
- **Exact integrity guarantee:** The full successfully written stream matches
  the expected digest, all writes/sync/close succeed before new publication,
  and any preexisting final object is independently hashed before success.
  Newly published content relies on faithful successful filesystem writes;
  there is no independent staging reread or physical-media certificate.
- **Failure classes caught:** Expected-digest mismatch, reader failure,
  interrupted ingestion, reported short/partial writes, and sync/close errors.
  Existing-object verification and explicit `Verify` detect observable stored
  digest mismatches. Silent staging corruption is not guaranteed to be detected
  before publication.
- **I/O cost:** Approximately N input read + N filesystem write for new
  ingestion, avoiding the additional N logical filesystem read and second hash.
  Existing-object verification still reads that object's full contents.
- **Rationale:** The reread's incremental coverage is silent mutation/corruption
  observable at that moment, not routine write failures or guaranteed media
  integrity. Mandatory linear read amplification is not justified by that
  limited coverage within the existing threat model.
