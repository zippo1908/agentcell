# Frontend development

The UI is a React + TypeScript + Vite SPA in `web/`, embedded into `celld`
via `go:embed` so the platform still ships as a single binary.

```sh
make web-install     # pnpm install (once)
make web             # typecheck + production build into web/dist
```

`web/dist` is **committed** so `go build ./...` works without Node installed;
CI rebuilds it and fails if the committed output is stale.

## Live development

Run celld (port-forwarded from a cluster, or locally against a kubeconfig)
on :8080, then:

```sh
cd web && pnpm dev      # http://localhost:5173, /api /preview /app proxied
```

## Layout

```
web/src
├── api/        client.ts (fetch + 401→/login) and types.ts (celld's JSON)
├── components/ Shell, PreviewPane, DispatchForm, SessionList, DiffView
├── pages/      CellsPage, CellPage (calibration loop), ReviewsPage
└── styles.css  tokens + light/dark, no CSS framework
```

State is server state, so it lives in TanStack Query (4s polling) rather
than a client store; the only local state is form input and which diff is
expanded.
