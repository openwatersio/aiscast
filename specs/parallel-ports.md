# Parallel worktrees without port conflicts

Several worktrees run the browser tests and dev servers at the same time on one machine. Each run picks free ports, so two runs never collide and nobody has to pick a port.

## Fixed ports this replaces

| Port | Who | Conflicts? |
|------|-----|-----------|
| 8787 TCP | e2e aiscast server ([client/e2e/server.sh](../client/e2e/server.sh)), named in [playwright.config.ts](../client/playwright.config.ts), [e2e/data.ts](../client/e2e/data.ts), and the `e2e` env in [wrangler.jsonc](../client/wrangler.jsonc) | Yes. The second run fails to start, or Playwright waits on the other worktree's `/health` and tests against the wrong server. |
| 8788 UDP | e2e volunteer receiver ([e2e/data.ts](../client/e2e/data.ts) `UDP_PORT`, server.sh `UDP_ADDR`) | Yes. The server logs the bind failure and keeps serving HTTP, so only the volunteer tests fail. |
| 4173 TCP | e2e `vite preview --strictPort` | Yes. |
| 5173 TCP | `npm run dev -w client` | No. Vite already moves to the next free port and prints it. |
| 9229 TCP | Cloudflare Vite plugin inspector | No. The plugin falls back to a free port. |
| 8080, 10110, 6060 | `go run .` in server/ | Yes, but only when a developer runs two local servers by hand. See "Out of scope". |

So the fix is the e2e harness. The client dev server already works.

## Design

Pick the three e2e ports once, in the Playwright runner, and pass them down through the environment. This is how [e2e/auth.ts](../client/e2e/auth.ts) already shares the token: the config runs in the runner and again in each worker, and the workers inherit the runner's environment.

1. **New `client/e2e/ports.ts`.** `e2ePorts(): { api: number; udp: number; app: number }`. If `AISCAST_E2E_API_PORT`, `AISCAST_E2E_UDP_PORT`, and `AISCAST_E2E_APP_PORT` are set, it returns them. If only some are set, it throws, because the lookup cannot avoid a pinned port it does not hold. Otherwise it finds free ports, writes them to `process.env`, and returns them. Free-port lookup has to be synchronous because the config is synchronous. It runs `execFileSync(process.execPath, ["-e", script])`, where the script binds TCP `127.0.0.1:0` twice and UDP `127.0.0.1:0` once with `node:net` and `node:dgram`, prints the three ports, and closes. No new dependency. Setting the variables yourself pins the ports, for example to match a server you started by hand.

2. **[playwright.config.ts](../client/playwright.config.ts).** Build `APP` and `API` from `e2ePorts()`. Pass `ADDR=127.0.0.1:<api>` and `UDP_ADDR=127.0.0.1:<udp>` in the server's `webServer.env`. Use `--port <app>` on `vite preview` and keep `--strictPort`, so a lost race fails loudly instead of serving on a port the tests don't know. Pass `AIS_API=http://127.0.0.1:<api>` in the preview's `env`, next to `AIS_TOKEN`.

3. **[e2e/server.sh](../client/e2e/server.sh).** Use `ADDR=${ADDR:?}` and `UDP_ADDR=${UDP_ADDR:?}` from the environment and drop the literals. The `:?` makes a direct run fail with a clear message instead of falling back to `:8080`.

4. **[e2e/data.ts](../client/e2e/data.ts).** `API` and `UDP_PORT` come from `e2ePorts()`. The workers inherit the runner's variables, so they all get the same ports.

5. **Render address.** Wrangler ignores the process environment whenever a `.dev.vars` or `.dev.vars.<env>` file exists, and the build copies that file into the preview. A developer's own `client/.dev.vars`, which CONTRIBUTING has them create, would win over the process environment and drop `AIS_TOKEN`. So the preview command writes `AIS_API` and `AIS_TOKEN` to `client/.dev.vars.e2e` before building. Under `CLOUDFLARE_ENV=e2e`, wrangler reads that file ahead of `.dev.vars`. `CLOUDFLARE_INCLUDE_PROCESS_ENV` is not needed. The `e2e` env in [wrangler.jsonc](../client/wrangler.jsonc) keeps `AIS_API` set to `http://127.0.0.1:0`, so typegen still sees a string and a lost override fails rather than rendering against production. `.gitignore` covers `.dev.vars*`.

6. **[client/README.md](../client/README.md).** Replace "on `127.0.0.1:8787` … on port 4173. Both ports must be free." with: the run picks free ports, and setting all three `AISCAST_E2E_*_PORT` variables pins them.

## Edge cases

- **Race between lookup and bind.** Another process can take a port after the lookup closes it and before the server or app binds it, which happens after their builds, up to a few minutes later. A taken TCP port fails the run at startup. A taken UDP port leaves the server up and fails only the volunteer tests. The odds are small. Accept it.
- **Two runs in one worktree** share `build/`, `.dev.vars.e2e`, and `e2e/.run`. `e2e/server.sh` keeps its pid, which becomes the server's, in `e2e/.run/lock` and stops a second run while that pid is alive. Playwright starts it before the app's build, so the second run touches nothing.
- **`reuseExistingServer`** stays off, as now. With per-run ports, a stale server from another worktree is never mistaken for this run's server. That mix-up is the real failure behind "8787 already in use."
- **`e2e/.run` state** is already per worktree, under `client/e2e/`, so parallel runs never share an archive.
- **CI** sets none of the variables and gets free ports on a fresh runner. No workflow change.
- **`storageState` origin** comes from `APP`, so it follows the chosen port.

## Verification

- Run `npm run test:e2e -w client` in two worktrees at once. Both pass, and `lsof -iTCP -sTCP:LISTEN | grep aiscast` shows two servers on different ports.
- Run one worktree with the `AISCAST_E2E_*_PORT` variables set and confirm it uses them.
- Run `sh e2e/server.sh` without `ADDR` and confirm it exits with the `:?` message, after its `go build` unless `AISCAST_BIN` is set.
- Two suites at once on one machine render maps in software side by side, so map-click and sheet-drag tests can time out under the load. Each suite passes alone.
- CI's `client-e2e` job passes unchanged.

## Out of scope

- **The Go server's own defaults** (`:8080`, `:10110`, `127.0.0.1:6060`). Production sets `ADDR` explicitly and Caddy, Alloy, and apply.sh expect 8080, so a "try the next port" fallback in `main.go` could hide a misconfigured box. A developer running two local servers sets `ADDR`, `UDP_ADDR`, and `PPROF_ADDR=off` on the second one and points `client/.dev.vars` at it. Add a fallback only if hand-run local servers keep colliding.
- **The client dev server.** Vite already picks the next free port.
