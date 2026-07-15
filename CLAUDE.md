# OpenStream Chat — agent conventions

- SPEC.md is the source of truth; when code and spec conflict, flag it, don't guess.
- AI/LLM features are v2 (SPEC.md §24) — never implement, stub, or document them.
- Every API mutation must: check permissions (internal/domain), respect
  channel-type flags (§6.1), write a transactional outbox event (§8.2), and be
  covered by a test asserting the emitted event.
- Errors always use the internal/apierror envelope; never fmt.Errorf to clients.
- DB access only through internal/store; all queries parameterized (pgx).
- Tests: table-driven; integration tests build-tagged `integration` and skip
  when OPENSTREAM_TEST_POSTGRES_DSN is unset; always run with -race.
- Run `make lint test` before declaring any task complete.
