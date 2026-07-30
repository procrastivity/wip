// Package store is wip's store: the single SQLite database that holds
// everything durable (D35, D49), and the data-access layer every verb reads
// and writes through.
//
// # Shape (D61)
//
// The store is **event-sourced**. D46 was closed by the `store-fork` Matter in
// favour of the event-sourced shape; `docs/store-fork/decision-record.md` is
// the record and MODEL D61 is the ratified entry. Concretely:
//
//   - The `events` table is the source of truth. It is append-only, enforced
//     by triggers rather than by the discipline of the code above it.
//   - Every other durable table — nodes, content, edges, gate state, the tier
//     rows, batches, dispatches, cursors, backlog — is a **projection**,
//     maintained in the same transaction as the append and rebuildable from
//     the log alone (Store.Rebuild).
//   - The one documented exception is Repo-tier *config*, including gate
//     declarations: declaring a gate is configuration, not an event (D4, D54),
//     so those tables are primary data and Rebuild leaves them alone.
//
// Two consequences are the whole point of the shape, and both are structural
// rather than conventional:
//
//   - A verb cannot express an event that is missing part of the envelope. It
//     returns Drafts carrying a type, a subject and a payload; the id, the
//     timestamp and the tier dimensions are stamped in exactly one place
//     (Store.stamp), so D56's "required dimensions are a static function of
//     event type" is one call made once, not a decision repeated per verb.
//   - There is no way to move a projection except by way of an event, because
//     applyEvent's only data argument is an event, and the live write path and
//     the rebuild path call the same function.
//
// # Provenance
//
// This package descends from the winning spike, `internal/spike/eventsourced`,
// which the `schema` Matter took as its literal starting point (its step-01).
// The spike stays in the tree as the evidence D61 cites — it is not live code
// and nothing here imports it. What `schema` added on top of it is recorded in
// `docs/schema/decisions.md`; the design of record is the `schema` Brief.
package store
