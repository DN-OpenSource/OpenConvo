// Package api implements the REST surface (SPEC.md §9): chi router,
// middleware chain (request id, logging, recovery, CORS, auth, rate
// limiting), Stream-compatible endpoints and the error envelope from
// internal/apierror. Every mutation checks permissions via internal/domain,
// respects channel-type flags and stages events through the transactional
// outbox (internal/store).
package api
