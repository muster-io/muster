# 0009. Single-page UI embedded in the binary

- Status: Accepted
- Date: 2026-10-02
- Amended: 2026-10-03 — the stream is `GET /api/v1/live-updates`, starts with `retry:` and ends with a final `401` when
  the session expires

## Context

Muster's UI is a client of its API (ADR-0008): the Alert Group list with filters and search, Timelines, configuration
of Integrations, Routes and Destinations, profiles with Account links, and live updates. The minimal installation should
stay "one binary plus PostgreSQL"; a separate frontend container would double the release artifacts, the versions to keep
in step and the deployment configuration. The UI must be translatable (English and Russian from the start), show times
in the user's time zone, and be typed end to end so that contributors and coding agents get compiler feedback rather
than runtime surprises.

The frontend landscape as checked in October 2026: TypeScript 7 (the native compiler) shipped without a programmatic
API, which arrives in 7.1; until then tools that import the compiler (typescript-eslint, openapi-typescript, hey-api) do
not work with it, while orval, oxlint and TanStack Router already do. Vite 8 builds with Rolldown. shadcn/ui builds on
Base UI by default.

## Decision

**Embedded SPA.** The UI is a React single-page application in `web/`, built to static files and embedded in the Go
binary with `go:embed`. There is one artifact; `make build` builds the UI before the binary. The SPA is served with a
Content Security Policy and the usual security headers.

**Stack.**

| Concern | Choice |
|---|---|
| Language, build, packages | TypeScript 7 (falling back to 6.0 is a configuration change), Vite 8, pnpm |
| UI runtime | React 19 |
| Routing | TanStack Router with file-based routes; the generated route tree is checked in and verified in CI; typed search parameters keep table filters in the URL |
| Components | shadcn/ui on Base UI, Tailwind CSS 4, lucide icons; copied MIT components keep their own license header (ADR-0001) |
| Server state | TanStack Query 5 |
| API client | orval, generated from `api/openapi.yaml`: a `fetch` client, Query hooks, Zod schemas and MSW mocks |
| Forms | React Hook Form 7 with Zod 4; the generated schemas are the only source of validation, with UI refinements on top; `Problem` errors from the server map onto form fields |
| Tables | TanStack Table 9 with TanStack Virtual |
| Live updates | the browser's `EventSource` on the server-sent events stream `GET /api/v1/live-updates`, fed by `LISTEN/NOTIFY`, which opens with `retry:` and is closed when the session ends (the reconnect gets `401`, which stops the browser); events are hints `{type, id}` that invalidate matching queries; after a reconnect everything is invalidated |
| Translations | i18next, react-i18next and i18next-cli; hierarchical keys; CI fails on a missing English or Russian translation |
| Dates | `Intl` for display, date-fns 4 with `@date-fns/tz` for arithmetic; Temporal later, through a polyfill, together with schedules |
| Lint and format | oxlint (type-aware) and oxfmt |
| Tests | Vitest in browser mode (real Chromium through Playwright) for components; Playwright end to end against `muster dev` with fake messenger and Alertmanager servers; MSW 2 for API mocks |

## Consequences

- Installation, upgrades and version skew are trivial: the UI and the API always match.
- The Go build depends on the frontend build; the Makefile and CI build both, and `make fmt lint test` gates both
  languages.
- One spec drives the server, the Go client, the TypeScript client, form validation and mocks; an API change surfaces
  as type errors in the UI.
- Starting on TypeScript 7 rules out typescript-eslint and some client generators until TypeScript 7.1; the fallback is
  TypeScript 6.
- Live updates carry hints, not data; the UI always re-reads through the API, so authorization stays in one place.
- Several pieces are young major versions (Vite 8, pnpm 12, TanStack Table 9); versions are pinned and upgrades are
  reviewed.

## Alternatives considered

- **A separate frontend container.** Two artifacts to version, release and deploy, with no benefit for users.
- **React Router 8.** The most widespread router, but typed routes only in its framework mode with its own conventions.
- **Mantine or another complete component library.** A faster start, but a foreign design system and a larger bundle;
  shadcn/ui keeps the component code in the repository.
- **openapi-typescript with openapi-fetch, hey-api, or Kubb** as the client generator. As checked in October 2026, the
  first two did not work with TypeScript 7 and openapi-typescript had seen few changes beyond dependency updates for
  several months; Kubb's then-current major release was very new and was maintained mostly by one person. These
  assessments may age; the choice can be revisited when the client generator is next reviewed.
- **TanStack Form.** A reasonable option, but both form libraries have a new major on the way and React Hook Form is
  better known.
- **ESLint with typescript-eslint, or Biome.** The former requires TypeScript 6; Biome's type-aware rules cover less.
- **Component tests on jsdom.** Miss bugs with portals and event propagation that a real browser catches.
