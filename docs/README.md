# Documentation

Everything written about this codebase besides the repository-root [`README.md`](../README.md)
(what it is, how to run it, every setting), `LICENSE`, and the two agent briefs
[`CLAUDE.md`](../CLAUDE.md) and [`AGENTS.md`](../AGENTS.md).

| Document | For | Read when |
|---|---|---|
| [`architecture.md`](architecture.md) | The one design document: topology, session path, the engine / lobby / finalize / shutdown contracts, package rules, invariants, and where to start reading the code | First, and before changing a contract |
| [`decisions.md`](decisions.md) | The ~20 choices that shape the code, each with context, cost and location. Numbers are stable ids that code comments cite | When the code looks wrong and you want to know whether it is deliberate |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | Adding a game, test conventions, size gates, commit and PR norms | Before your first pull request |
| [`deployment.md`](deployment.md) | The runbook from a fresh VPS to a hardened, running instance | Before you deploy and before each update |
| [`SECURITY.md`](SECURITY.md) | Threat model, reporting a vulnerability, scope, and the hardening checklist for a deployment you run | Before you report something or self-host |
| [`data-inventory.md`](data-inventory.md) | Every piece of personal data the deployment holds, where, and for how long | When somebody asks a data question |
| [`deferred.md`](deferred.md) | Design work deliberately not done, each with the trigger that brings it back | Before a refactor that looks overdue |

The privacy policy and terms of service are not mirrored here: the published pages,
<https://www.tty.cards/privacy/> and <https://www.tty.cards/terms/>, are the only copy
(sources in `web/src/legal/`).

## Start here

**To work on the code:** run it ([`../README.md`](../README.md), ten minutes), read
[`architecture.md`](architecture.md) once, skim [`decisions.md`](decisions.md), then follow
the reading order at the end of `architecture.md` with the code open. [`CONTRIBUTING.md`](CONTRIBUTING.md)
before your first PR.

**To operate it:** [`deployment.md`](deployment.md), then the checklist in
[`SECURITY.md`](SECURITY.md).

**If you are an agent:** [`../CLAUDE.md`](../CLAUDE.md) is the short operational brief.

## Which document owns which fact

When two documents disagree, the code wins, and this ordering says where the fix belongs.

| Fact | Owner |
|---|---|
| Contracts, invariants, package responsibilities, topology | [`architecture.md`](architecture.md) |
| The *reason* a choice was made | [`decisions.md`](decisions.md) |
| Commands, ports, environment variables | [`../README.md`](../README.md) |
| Test and PR conventions, adding a game | [`CONTRIBUTING.md`](CONTRIBUTING.md) |
| Deploying and hardening | [`deployment.md`](deployment.md), [`SECURITY.md`](SECURITY.md) |
| What personal data exists and for how long | [`data-inventory.md`](data-inventory.md) |
