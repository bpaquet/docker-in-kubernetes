# docker-in-kubernetes — Design

## Goal

A Go program that exposes a Docker-compatible HTTP API on a UNIX socket. When a user sets `DOCKER_HOST=unix:///path/to/docker-in-kubernetes.sock`, the standard `docker` CLI transparently drives pods on the user's currently-configured Kubernetes cluster instead of a local Docker daemon.

Primary use case: `docker run -p 6379:6379 redis` creates a Pod on the cluster and forwards `localhost:6379` to the pod.

## Prior work

The idea has been attempted; nothing maintained covers the exact "unmodified Docker CLI → remote k8s" niche.

| Project | Overlap | Why it doesn't cover this |
|---|---|---|
| **k3c** (Darren Shepherd, ~2020) | Docker-like CLI on top of k8s, Docker-ish API | Archived, never reached parity, build-focused. Worth reading the source for protocol skeleton. |
| **Telepresence** | Local ↔ cluster bridging, SPDY port-forwarding | Inverse direction: local code talks to cluster services. Doesn't run `docker run` against a cluster. |
| **Okteto / DevSpace / Tilt / Skaffold** | "Dev loop in k8s" | All require their own manifests/config. None lets unmodified `docker run` work. |
| **nerdctl** | Docker-compatible CLI, k8s-aware namespaces | Backend is local containerd, not remote k8s. |
| **podman-remote** | Docker-compatible socket | Backend is podman; socket layer is reusable architecture inspiration. |
| **kompose** | docker-compose.yml → k8s YAML | Static translation, no runtime. |
| **kubectl run / debug** | One-shot "docker run"-like UX | No socket, no API, no `docker ps`. |
| **devpod** | Devcontainers on remote backends including k8s | Solves the workspace-sync problem we'd otherwise hit; useful prior art for the devcontainer angle. |

**Differentiator**: every tool that already speaks `DOCKER_HOST` (Testcontainers, IDE Docker integrations, `act`, CI helpers) should just work with zero config change. None of the above delivers that.

**Reusable Go libraries** — do not hand-roll:
- `github.com/docker/docker/api/types` — official Docker API JSON shapes so `inspect` matches byte-for-byte.
- `github.com/docker/docker/pkg/stdcopy` — multiplexed exec stream framing.
- `k8s.io/client-go/tools/portforward` — SPDY port-forward (already planned).

## Compatibility with Testcontainers and Devcontainers

Short version — informs scope; full matrix to be built as we implement.

### Testcontainers — mostly feasible, with sharp edges

A v1 plus a few additions can run the common Testcontainers modules (Redis, Postgres, Kafka). Additions needed beyond the v1 surface:

- `/containers/{id}/archive` (tar PUT/GET) for `withCopyFileToContainer` — proxy via exec+tar.
- `/networks/*` — no-op stubs (our namespace *is* the network).
- Random host-port allocation when `HostPort` is empty in `PortBindings`.
- Byte-accurate `inspect.NetworkSettings.Ports`.
- Stubbed `/images/create` (rely on cluster pull-on-create).

**Known blocker**: the **Ryuk reaper** container connects *back* to the Docker socket from inside the cluster — impossible when the socket lives on the user's laptop. Mitigation: require `TESTCONTAINERS_RYUK_DISABLED=true`. Aligns with our "no cleanup on shim exit" stance.

**Smoke-tested**: `internal/integration/testcontainers_test.go` drives `testcontainers-go` against the daemon — spawns `redis:7-alpine`, resolves `Endpoint()` to a forwarded `127.0.0.1:port`, and exercises PING/SET/GET via the real Go redis client.

### Devcontainers — not pursued

Two fundamental blockers:

1. **Workspace bind mount**: `devcontainer.json` mounts the laptop's source folder into the pod. A pod in a remote cluster can't see that filesystem without a sync layer (Mutagen / rsync sidecar). Out of scope.
2. **`docker build` and Features**: devcontainers leans heavily on build; we declared build out of scope.

`devpod` already solves this with its own provider abstraction. Users who need devcontainers-on-k8s should use that.

## First milestone

`docker run -d -p 6379:6379 redis` works end-to-end against a real cluster:

1. `docker -H unix:///path/to/docker-in-kubernetes.sock run -d -p 6379:6379 redis` returns a container ID.
2. A Pod appears in the configured namespace, reaches `Ready`.
3. `redis-cli -h 127.0.0.1 -p 6379 ping` returns `PONG`.
4. `docker ps` shows the container.
5. `docker logs <id>` streams the redis startup logs.
6. `docker rm -f <id>` deletes the pod and closes the forwarder.

`-it` (interactive + TTY), `docker exec`, `docker stop`/`start` round-trip, and Testcontainers compatibility are follow-up milestones.

## `docker run -d` blocking semantics

`docker run -d` blocks until the container is fully usable — closer to Docker's UX than a fire-and-forget. Sequence:

1. `POST` the Pod.
2. Watch pod events. **Fail fast** if the pod enters `ImagePullBackOff` / `ErrImagePull` / `CreateContainerError` — return a non-zero error to the CLI (with the k8s reason in the message). 30s overall timeout as backstop.
3. Wait for `Pod.Status.Phase == Running` **and** `condition Ready == True`.
4. Open the port-forwarder(s).
5. Return the container ID to the CLI.

Non-detached `docker run` (no `-d`) is **not supported in v1** — the daemon returns a clear "interactive run not supported, use -d" error. Reason: requires HTTP hijack + attach stream plumbing that belongs in the same milestone as `docker exec` / `-it`.

## Container ID

64-hex Docker-compatible ID, derived deterministically from the pod identity:

```
id = sha256("<namespace>/<podname>")  // 64 hex chars
shortID = id[:12]                     // matches Docker CLI display
```

Stable across the pod's lifetime, regenerable from `ps` with no daemon state. Reverse lookup (`id → pod`) is a label-selector list + filter on the daemon side.

## Quality bar

This is production-grade code, not a prototype.

- **Unit tests** on every package. Table-driven where natural. `testify/require` for assertions.
- **K8s interactions** tested with `k8s.io/client-go/kubernetes/fake` — no hand-rolled mocks at the wrapper layer.
- **Pod-spec builder, name sanitization, container ID derivation, port-binding parsing** are pure functions — fully unit-testable, no I/O.
- **End-to-end tests** under `internal/integration` (`//go:build integration`) drive the real `docker` CLI against the daemon's UNIX socket; the daemon points at a dedicated `kind` cluster whose kubeconfig lives at `./tmp/integration-kubeconfig` (never `~/.kube/config`). Tasks: `mise run integration-up`, `integration-test`, `integration-down`. Coverage: docker version / ping / run -d / ps / logs / inspect / kill / rm (incl. rm-of-already-gone) / port-forward round trip / image pull fail-fast / non-detached run rejection.
- **CI**: `gofmt -d`, `go vet`, `golangci-lint run`, `go test ./...` on every push. CI must be green before merge.
- **No silent errors**. Wrap with `%w`; surface k8s reasons in messages returned to the CLI.
- **Coverage** is a side-effect of "test the things that matter", not a target. Don't pad with trivial getter tests.

## Toolchain

- **Go**: 1.26 (k8s.io/client-go v0.36 requires it; also gets `t.Context()`).
- **HTTP server**: stdlib `net/http`. No router framework.
- **Logging**: `log/slog` with a text handler; level via `--log-level` flag (default `info`).
- **K8s client**: `k8s.io/client-go v0.36`.
- **golangci-lint**: 2.12.2 (needs to be built with a Go ≥ the module's directive — 2.1.6 from PR #1 was Go 1.24 and could not parse Go 1.26 export data).
- **Local integration cluster**: `kind v0.32` + `kubectl 1.32`, both pinned in `mise.toml`.

## Container name sanitization

K8s pod names are RFC 1123: lowercase alphanumerics + `-`, max 63 chars. Docker `--name` is more permissive. Translation rule (`internal/podspec.SanitizeName`):

- Lowercase the input.
- Replace every run of non-alphanumeric characters with a single `-` (collapses both invalid chars and existing separators in one pass).
- Trim leading/trailing `-`.
- Truncate to 63 chars and re-trim a trailing `-`.
- Store the original `--name` in annotation `docker-in-kubernetes.io/docker-name` so `docker ps`/`inspect` round-trip the user's chosen name.

When no `--name` is given, `internal/podspec.GeneratedName` returns `dik-<image-base>-<hex6>` (e.g. `dik-redis-7af34c`).

## Image primitives

The cluster handles the real pull on pod creation. The daemon keeps an in-memory `images.Store` (ref + tag + pulled-at) so the CLI's image workflow round-trips end-to-end against recorded refs. Store lives only in process memory — lost on restart, never persisted.

| Docker CLI                  | Engine endpoint                          | Behaviour |
| --------------------------- | ---------------------------------------- | --------- |
| `docker pull <ref>`         | `POST /images/create`                    | Records the ref. Streams two jsonmessage lines (`Pulling from <ref>`, `Status: Image is up to date for <ref>`). |
| `docker images`             | `GET /images/json`                       | Lists recorded refs as `ImageSummary` (one entry per ref). `Containers: -1` (we don't track usage). |
| `docker image inspect <n>`  | `GET /images/<name>/json`                | Resolves name → ref by exact match, `<name>:latest` fallback, or unique short-ID hex prefix. 404 if missing. |
| `docker image rm <n>`       | `DELETE /images/<name>`                  | Same resolution. Returns `[{Untagged: <ref>}, {Deleted: <id>}]`. 404 if missing. |

**Image ID** is derived deterministically: `id = "sha256:" + hex(sha256(<ref>))`. Same scheme as container IDs — no extra state, regenerable from the ref alone.

## Network primitives

The daemon's k8s namespace IS the network — every pod in it can reach every other pod by name via cluster DNS. We don't model `docker network` shapes, we just answer them so `docker compose up` (which probes `GET /networks/<project>_default` before creating containers) doesn't trip. An in-memory `networks.Store` keyed by name backs the responses; like images, state is lost on restart.

| Docker CLI                          | Engine endpoint                              | Behaviour |
| ----------------------------------- | -------------------------------------------- | --------- |
| `docker network create <n>`         | `POST /networks/create`                      | Records the network. Returns `{Id, Warning}`. |
| `docker network ls`                 | `GET /networks`                              | Lists recorded networks. |
| `docker network inspect <n>`        | `GET /networks/{name}`                       | 404 if missing — drives compose's create-on-missing flow. |
| `docker network rm <n>`             | `DELETE /networks/{name}`                    | 204 / 404 if missing. |
| `docker network connect/disconnect` | `POST /networks/{name}/(dis)connect`         | No-op 200 — pods already see each other. |

**Network ID** is `hex(sha256(name))` (no `sha256:` prefix to match Docker's network ID format).

## Event stream

`GET /events` holds the connection open and emits nothing. Compose subscribes to it for the lifetime of `compose up`; closing early triggers reconnect loops and log noise. Modelling real pod lifecycle events as Docker-shaped events is a follow-up — a quiet stream is enough for compose, `docker events --until=…` (window-based exit), and any tool that just wants the subscription to succeed.

## Non-goals (v1)

- Image build/push management (`docker build`, `docker push`). `docker pull` / `images` / `image rm` / `image inspect` are stubbed against an in-memory store — see [Image primitives](#image-primitives).
- Volume mounts (`-v`) and bind mounts.
- Real `docker network` semantics — endpoints are stubbed (see [Network primitives](#network-primitives)) so compose's pre-create probe passes; the namespace itself provides connectivity.
- Compose, swarm, plugins.
- Restart policies beyond `Never`.
- Multi-container pods.
- Cleanup of pods on shim exit — pods persist; user is responsible.

## Module

- Repo: `github.com/bpaquet/docker-in-kubernetes`
- Binary: `docker-in-kubernetes`

## Architecture

```
+----------------+   HTTP over UNIX socket   +------------------------+   client-go   +-----------+
|  docker CLI    | ------------------------> | docker-in-kubernetes   | ------------> |  K8s API  |
|  (unchanged)   | /v1.43/containers/create  |        daemon          |  Pods, exec,  |  server   |
+----------------+ /containers/{id}/start... +------------------------+  portforward  +-----------+
                                                        |
                                                        | SPDY port-forward
                                                        v
                                                  localhost:HOSTPORT
```

The daemon implements a subset of the Docker Engine HTTP API. Each Docker container maps 1:1 to a Kubernetes Pod in a single configured namespace.

## API surface (v1)

Real Docker Engine HTTP API, enough that the unmodified `docker` CLI works for:

| Docker CLI                         | Engine endpoints                                                            |
| ---------------------------------- | --------------------------------------------------------------------------- |
| `docker run -p -e --name -d`       | `POST /containers/create`, `POST /containers/{id}/start`                    |
| `docker ps`, `docker ps -a`        | `GET /containers/json`                                                      |
| `docker inspect`                   | `GET /containers/{id}/json`                                                 |
| `docker stop`, `docker kill`       | `POST /containers/{id}/stop`, `POST /containers/{id}/kill`                  |
| `docker rm`                        | `DELETE /containers/{id}`                                                   |
| `docker logs`                      | `GET /containers/{id}/logs`                                                 |
| `docker exec`                      | `POST /containers/{id}/exec`, `POST /exec/{id}/start`, `GET /exec/{id}/json`|
| `docker version`, `docker info`    | `GET /version`, `GET /info` (live container counts; version metadata wired from `-ldflags`) |
| `docker pull`, `docker images`, `docker image rm`, `docker image inspect` | `POST /images/create`, `GET /images/json`, `DELETE /images/{name}`, `GET /images/{name}/json` — see [Image primitives](#image-primitives) |
| `docker network create/ls/inspect/rm/connect/disconnect` | `POST /networks/create`, `GET /networks`, `GET /networks/{name}`, `DELETE /networks/{name}`, `POST /networks/{name}/(dis)connect` — see [Network primitives](#network-primitives) |
| `docker events`, `docker compose up` subscription | `GET /events` — see [Event stream](#event-stream) |

API version: advertise `1.43` (Docker Engine 24.0) via `/_ping` `Api-Version` header and `/version`. Accept any `/v1.x` prefix and route to the same handlers. This covers the modern `docker` CLI's negotiation floor and Testcontainers' `>= 1.24` minimum without obliging us to implement post-1.43 endpoints.

`/version` fills `Version`, `GitCommit`, and `BuildTime` from `-ldflags "-X main.version=... -X main.gitCommit=... -X main.buildTime=..."` (the Makefile derives them from `git describe`, `git rev-parse`, and `date -u`). `/info` reports live `Containers` / `ContainersRunning` counts by listing managed pods on each call; `NCPU` is `runtime.NumCPU()`. We do not advertise `KernelVersion` or `MemTotal` — they're host-level concepts and meaningless when the daemon runs against a remote cluster.

Container exit state surfaces through both `/containers/json` and `/containers/{id}/json`: the real exit code and finish time come from `pod.Status.ContainerStatuses[0].State.Terminated`. `ps` shows `Exited (N) <duration> ago`; `inspect` populates `State.ExitCode` and `State.FinishedAt`.

## Container ↔ Pod mapping

- **1 container = 1 Pod**, single container inside the pod.
- **Pod name**: sanitized `--name` if provided; otherwise `dik-<image-base>-<hex6>` (see *Container name sanitization*).
- **Docker container ID**: `sha256("<namespace>/<podname>")` rendered as 64 hex chars; first 12 chars used for the short ID.
- **Pod spec**:
  - `restartPolicy: Never`
  - One container, image = Docker image as-is
  - `env` ← `-e`
  - `ports` ← `-p` (containerPort only; host port handled by port-forward, see below)
- **Labels** (used by `ps` to find "our" pods):
  - `docker-in-kubernetes.io/managed=true`
  - `docker-in-kubernetes.io/container-name=<docker name>`
  - `docker-in-kubernetes.io/project=<project>` (defaults to `default`; reserved for Compose v2)
- **Annotations**:
  - `docker-in-kubernetes.io/created-at=<RFC3339>`
  - `docker-in-kubernetes.io/image=<original image string>`
  - `docker-in-kubernetes.io/docker-name=<original --name value>`
  - `docker-in-kubernetes.io/ports=<json of -p mappings>` (only when non-empty)
  - `docker-in-kubernetes.io/env=<json of -e>` (only when non-empty; for `docker inspect` fidelity)
  - `docker-in-kubernetes.io/labels=<json of -l user labels>` (only when non-empty)
  - `docker-in-kubernetes.io/user=<original --user value>` (only when set; for `docker inspect`)
  - `docker-in-kubernetes.io/memory=<bytes>` and `docker-in-kubernetes.io/nano-cpus=<billionths>` (only when set; round-trip the originals through `docker inspect`)

### Resources and user

- `--memory` / `-m` → `container.resources.requests.memory` *and* `.limits.memory` (request == limit). Decoded back from the `memory` annotation for `docker inspect`'s `HostConfig.Memory` (bytes).
- `--cpus` → `container.resources.requests.cpu` *and* `.limits.cpu` in milli-cores (ceil of `nanoCPUs / 1_000_000`). Decoded back from the `nano-cpus` annotation for `HostConfig.NanoCpus`.
- `--user` → `container.securityContext.runAsUser` (and `runAsGroup` when `uid:gid` is given). Numeric only — k8s can't resolve container-side usernames; non-numeric input is rejected with HTTP 400 at `/containers/create`.

## Port forwarding

docker-in-kubernetes picks one of two forwarder backends at startup, based on whether it runs inside the cluster.

**Mode detection**: presence of `KUBERNETES_SERVICE_HOST` env var → in-cluster mode (also triggers `rest.InClusterConfig()` for k8s auth). Otherwise → local mode.

### Local mode (laptop)

- In-process port-forward using `k8s.io/client-go/tools/portforward` over SPDY (no `kubectl` dependency).
- For every `-p HOST:CONTAINER` on `docker run`, the daemon:
  1. Waits for the pod to reach `Ready`.
  2. Opens a port-forward goroutine binding `127.0.0.1:HOST → pod:CONTAINER` via the apiserver.
  3. Tracks the forwarder in an in-memory map keyed by container ID.

### In-cluster mode (docker-in-kubernetes running as a pod)

- No SPDY port-forward — the docker-in-kubernetes pod has direct network access to other pods in the cluster.
- For every `-p HOST:CONTAINER`, the daemon:
  1. Waits for the pod to reach `Ready` and have a `PodIP`.
  2. Starts a plain TCP proxy goroutine: `Listen(127.0.0.1:HOST) → Dial(podIP:CONTAINER)`.
  3. Tracks the proxy in the same in-memory map.
- Listener always binds `127.0.0.1` inside the docker-in-kubernetes pod (matches local-mode semantics; reachable only from inside the docker-in-kubernetes pod).
- Lower overhead, no apiserver round-trip per byte, no SPDY framing.

### Common

- On `stop`/`kill`/`rm`, the forwarder is cancelled regardless of mode.
- A per-container background watcher polls the pod (2s default) and closes the forwarder when the container terminates or the pod is gone — so naturally-exiting containers free their host port without needing an explicit `docker rm`.
- Forwarders are **not** restored across daemon restarts in v1 (pods persist, forwards do not). Re-running `docker start` would re-establish. See [Later](#later-deferred-features) for the planned auto-rebuild.

## Lifecycle

| Docker verb | What docker-in-kubernetes does                                                              |
| ----------- | -------------------------------------------------------------------------- |
| `create`    | Build the Pod spec and stash it in an in-memory pending store. The pod is **not** posted to k8s yet. |
| `start`     | Realize the staged pod via `POST /api/v1/.../pods`, then block on `Ready` and open port-forwards. No-op if already running. |
| `stop`      | `DELETE` Pod with graceful `terminationGracePeriodSeconds` (default 10s). Close forwards. |
| `kill`      | `DELETE` Pod with `gracePeriodSeconds=0`. Close forwards.                  |
| `rm`        | Drop a pending entry, or delete the pod. No-op if neither exists.          |
| `logs`      | Stream from `GET /api/v1/namespaces/{ns}/pods/{name}/log?follow=true`.     |
| `attach`    | Hijack the connection. For a pending container, write the upgrade headers immediately (so the CLI's subscription is "established"), then wait for `/start` to realize the pod, then wait for kubelet to actually start the container before opening SPDY-attach or the log stream. |
| `exec`      | `POST .../pods/{name}/exec` SPDY stream, multiplexed through Docker's exec protocol. |
| `wait`      | Flush response headers up front (so `docker run -d`'s wait subscription "completes"), then block until the container exits. For `condition=removed`, also delete the pod after exit — basis of `docker run --rm`. |

### Deferred create → start

Docker's CLI for `docker run`/`docker run --rm` issues `/create` → `/attach` → `/start` → `/wait` in that order. If `/create` realized the pod immediately we would race the attach subscription against kubelet container start, and for TTY+stdin pods we would deadlock because kubelet only flips `Ready=True` once attach connects.

Instead `/create` only stages the pod spec in an in-memory pending store. `/start` is the call that actually `POST`s the pod to k8s. `/attach` and `/wait` against a pending container hijack/flush their response headers (CLI considers the subscription established), then block on a per-container signal until `/start` lands. `/start` signals waiters as soon as `pods.Create` succeeds — not after `WaitForReady` — so attach can connect while readiness is still settling.

## State

- **No persistent state in the daemon.** All truth lives in the k8s API server.
- `ps` lists pods with label `docker-in-kubernetes.io/managed=true` in the configured namespace.
- Port-forwarders are in-memory only; lost on daemon restart.

## Configuration

CLI flags:

- `--socket` (default `/tmp/docker-in-kubernetes.sock`): UNIX socket path
- `--namespace` (default `docker-in-kubernetes`): k8s namespace for all pods
- `--kubeconfig`: path to kubeconfig (default: `KUBECONFIG` env, else `~/.kube/config`)
- `--context`: kubeconfig context (default: current context)
- `--log-level` (default `info`): `debug` / `info` / `warn` / `error`

When `KUBERNETES_SERVICE_HOST` is set, docker-in-kubernetes uses in-cluster config and ignores `--kubeconfig`/`--context`.

## Forward compatibility: Docker Compose (v2, not implemented yet)

v1 must not foreclose Compose support. Decisions taken now to leave the door open:

- **Service discovery via namespace**: Because docker-in-kubernetes runs in a single dedicated namespace (`--namespace`), in-cluster DNS already provides cross-pod resolution (`<podname>.<ns>.svc.cluster.local`, or `<podname>` from within the same ns once a headless Service exists). v2 will add a headless Service per pod or a shared one; v1 does not need to model "networks" explicitly — the dedicated namespace *is* the network.
- **Project label on every resource**: every Pod (and future Service/PVC) carries `docker-in-kubernetes.io/project=<name>` (defaults to `default` for plain `docker run`). v2's `compose down` becomes a label-selector delete. v1 must already write this label so v2 can pick up pre-existing pods.
- **Volumes mapped to k8s storage**: named volumes → PVC (`docker-in-kubernetes-vol-<project>-<name>`), anonymous → `emptyDir`, host bind mounts → explicitly rejected with a clear error. Not implemented in v1, but the pod-spec builder is structured so a volume list can be slotted in without redesign.
- **depends_on / ordered startup**: deferred to v2.

These choices mean v1's pod labels and namespace assumption are forward-compatible; v2 layers Compose semantics on top without migrating existing resources.

## Project layout

```
cmd/docker-in-kubernetes/main.go              # flag parsing, daemon bootstrap
internal/server/             # HTTP router, Docker API handlers
  containers.go              #   create/start/stop/kill/rm/inspect/list
  logs.go                    #   logs streaming
  exec.go                    #   exec protocol
  version.go                 #   version/info
internal/k8s/                # client-go wrappers
  pods.go                    #   pod spec builder, CRUD
  portforward.go             #   SPDY port-forward manager
  exec.go                    #   SPDY exec
internal/model/              # Docker API JSON types we emit
go.mod
```

## Later (deferred features)

Explicitly out of scope for v1, planned for later iterations. Listed here so v1 design choices keep them feasible.

### Auto-rebuild port-forwards on daemon startup

On startup, scan the configured namespace for pods with `docker-in-kubernetes.io/managed=true` in phase `Running`, read each pod's `docker-in-kubernetes.io/ports` annotation, and re-open every port-forward. Makes the daemon feel stateless from the user's view: restart `docker-in-kubernetes`, ports come back.

Behaviour to nail down when we implement it:

- **Port conflicts on rebuild**: if `127.0.0.1:HOST` is already taken, log + skip that single forwarder; do not abort startup.
- **Pod phase filter**: only `Running` pods get forwards rebuilt. `Pending`/`Succeeded`/`Failed` are listed by `ps` but get no forward until a `docker start`.
- **Partial failures**: one pod's failure must not block the others — log and continue.

### Docker Compose compatibility (v2)

See [Forward compatibility: Docker Compose](#forward-compatibility-docker-compose-v2-not-implemented-yet) for the v1 design choices that keep this open (project label, namespace = network, volume scheme). The actual implementation — parsing `docker-compose.yml`, `compose up/down/ps`, `depends_on` ordering — is v2.

### Other open questions

- Cleanup on exit. Lease-based GC is the most idiomatic if/when we add it.
- `-v` volumes → `emptyDir` / hostPath / PVC.
- `--restart` flag mapping.
- Multi-arch images, private registries with `imagePullSecrets`.
- Concurrent port allocation collisions (two `docker run -p 6379:...` calls).
- TLS/auth on the UNIX socket (currently relies on filesystem perms).
