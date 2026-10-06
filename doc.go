// Package cask provides verified filesystem content-addressable storage for
// immutable blobs, keyed by explicitly typed SHA-256 or SHA-512 digests.
//
// Put requires an expected digest and hashes the incoming stream while checking
// writes, sync, and close. It atomically publishes a completed object without
// replacing an existing object. Existing objects are hashed before duplicate Put
// succeeds. New objects are not reread; successful writes are assumed faithful.
// Open does not hash; Verify explicitly checks stored bytes at that moment.
// Digest identity is not publisher authenticity or a permanent integrity proof.
//
// Stores require an existing trusted local root directory and filesystem support
// for atomic no-clobber publication. Namespace atomicity and file sync are not a
// guarantee of namespace persistence across power loss. Processes with malicious
// write access, hostile filesystem manipulation, and network filesystems are
// outside the contract. Close must not race with store methods; readers returned
// by Open remain owned by their callers and survive Store.Close.
package cask
