// Package store is the persistence layer: PostgreSQL access via pgx,
// versioned SQL migrations, the transactional outbox and the bounded event
// log (SPEC.md §2.3, §4). All SQL lives here; queries are parameterized —
// no SQL is ever built from unvalidated user input (the filter compiler in
// store/filters whitelists fields and operators).
package store
