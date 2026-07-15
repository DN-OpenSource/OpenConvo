// Package worker hosts background jobs (SPEC.md §2.2): the transactional
// outbox relay, retention sweeps (event log, expired bans/pins) and message
// partition maintenance. Push, webhook delivery and media pipelines attach
// here as they land.
package worker
