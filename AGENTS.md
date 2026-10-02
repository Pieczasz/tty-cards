# AGENTS.md

Conventions for coding agents working in this repository. The operational brief -
layer boundaries, the engine/rules contract, the turn clock, the SSH server, the
conventions you are expected to follow - is **[`CLAUDE.md`](CLAUDE.md)**. Read it
first; it is the canonical short form and this file does not repeat it.

| You need | Read |
|---|---|
| The rules you must not break | [`CLAUDE.md`](CLAUDE.md) |
| What every document is for, and the order | [`docs/README.md`](docs/README.md) |
| Contracts, topology, the invariant list | [`docs/architecture.md`](docs/architecture.md) |
| Why a non-obvious thing is the way it is | [`docs/decisions.md`](docs/decisions.md) |
| Where to start reading, in order | [`docs/reading-guide.md`](docs/reading-guide.md) |
| How to add a game, test and commit norms | [`docs/CONTRIBUTING.md`](docs/CONTRIBUTING.md) |
| Commands, ports, configuration | [`README.md`](README.md) |

Three habits that matter more here than the average repo:

1. **Every fix ships a test that fails on the old code.** Not one that passes after.
2. **Do not raise a `.golangci.yml` threshold to land a change.** Split the function.
3. **Comments say *why*.** If a guard is unreachable, delete it and say so - do not
   document it as defence.
