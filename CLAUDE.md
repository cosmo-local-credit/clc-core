# clc-core

Go backend for the sibling `../clc-app`; its `packages/core` is a separate
TypeScript domain package. Read each changed repository's guidance and validate
both sides of an API change.

- Keep discovery SQL in named query files. Explicitly type numeric parameters
  where PostgreSQL inference could narrow them; test fractional coordinates,
  boundary points, and antimeridian bounds through the real driver.
- Reproduce database bugs with PostgreSQL-backed tests and API bugs through the
  real route/store boundary. Use isolated fixtures and assert expected data,
  including empty and clustered responses; execution coverage is supporting
  evidence rather than a correctness guarantee.
- Treat `ADMIN` and `SUPERADMIN` as administrative roles. Keep account-role
  mutation `SUPERADMIN`-only, and revalidate privileged JWT claims against the
  current database role before authorizing a protected request.
- Run `gofmt`, `golangci-lint run ./...`, focused database/query-plan tests, and
  `CORE_TEST_DSN=... go test -p 1 ./... -count=1 -timeout 900s` against an isolated
  local database. Confirm the relevant tests pass rather than skip. Serial
  packages prevent committed API fixtures from interfering with store tests.
- Validate app changes in `../clc-app` as well. Record fixture-based browser and
  actual deployment checks separately; map rendering checks need working WebGL.
