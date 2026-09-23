# Documentation

Everything written about this codebase, except the repository-root
[`README.md`](../README.md) (orientation and commands), `LICENSE`, and the two
agent briefs [`CLAUDE.md`](../CLAUDE.md) and [`AGENTS.md`](../AGENTS.md).

The goal of this set is that a newcomer can read from the beginning and come out
understanding the codebase, its design, and every non-obvious choice - so the
project can live without its author.

## What each document is for

| Document | For | Read when |
|---|---|---|
| [`architecture.md`](architecture.md) | **The canonical design document.** What the system is, the topology, the session lifecycle, the engine contract, the lobby state machine, ratings, persistence, the layout budget, the security model, observability, and the invariant list | First, and again whenever you are about to change a contract |
| [`codebase-map.md`](codebase-map.md) | C4 and UML diagrams of the whole system, what is absent on purpose, open items and candidate next steps. A map that links to the owners below | First, for the picture; then follow its links |
| [`decisions.md`](decisions.md) | One record per non-obvious choice: context, decision, consequences, where in the code. Forty-eight of them | When the code looks wrong and you want to know whether it is deliberate |
| [`reading-guide.md`](reading-guide.md) | An ordered, bottom-up walk through the Go packages. Per step: the files and their size, what to understand, the invariant to check, the one test that teaches it | With the code open, on your first few days |
| [`onboarding.md`](onboarding.md) | The product story, the annotated file tree, and the three ways to run it locally | Day one, before you write anything |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | How to add a game, the test conventions, the size gates, commit and PR norms | Before your first pull request |
| [`SECURITY.md`](SECURITY.md) | Reporting a vulnerability, what is in and out of scope, and how to harden a deployment you run | Before you report something, or before you self-host |
| [`data-inventory.md`](data-inventory.md) | Every piece of personal data this deployment holds, where it lives and for how long, with the file that proves each claim | When somebody asks a data question |
| [`privacy.md`](privacy.md) | The published Privacy Policy, mirroring <https://www.tty.cards/privacy/> | As the user-facing version of the inventory |
| [`terms.md`](terms.md) | The published Terms of Service, mirroring <https://www.tty.cards/terms/> | |
| [`changelog.md`](changelog.md) | What changed, in terms a player or an operator would care about | |

## The recommended path

**If you are going to work on the code**, in this order:

1. [`../README.md`](../README.md) - get it running and play a hand. Ten minutes.
2. [`onboarding.md`](onboarding.md) - what the product is and how the tree is
   laid out.
3. [`architecture.md`](architecture.md) §1-§4 - the shape of the system and the
   four contracts that bite.
4. [`reading-guide.md`](reading-guide.md) - the actual walk, bottom-up, with the
   code open. This is the longest step and the one that matters.
5. [`architecture.md`](architecture.md) §5-§12 - money, persistence, ratings,
   security, and the invariant list, now that the names mean something.
6. [`decisions.md`](decisions.md) - skim it once end to end. You will come back
   to individual records.
7. [`CONTRIBUTING.md`](CONTRIBUTING.md) - before you open a pull request.

**If you are here to answer one question**, use the "where is X?" index at the
bottom of [`reading-guide.md`](reading-guide.md).

**If you are here to operate it**, read [`../README.md`](../README.md)
"Self-hosting", then [`SECURITY.md`](SECURITY.md) "Hardening the deployment you
run", then [`architecture.md`](architecture.md) §2 and §10.

**If you are an agent**, [`../CLAUDE.md`](../CLAUDE.md) is the short operational
brief; this set is the long form behind it.

## Which document owns which fact

When two documents disagree, the code wins - and then this ordering decides where
the fix belongs.

| Fact | Owner |
|---|---|
| Contracts, invariants, package responsibilities, topology | [`architecture.md`](architecture.md) |
| The *reason* a choice was made | [`decisions.md`](decisions.md) |
| The order to read files in, and the tests that teach them | [`reading-guide.md`](reading-guide.md) |
| The annotated file tree and the product story | [`onboarding.md`](onboarding.md) |
| Commands, ports, environment variables | [`../README.md`](../README.md) |
| Test and PR conventions, adding a game | [`CONTRIBUTING.md`](CONTRIBUTING.md) |
| What personal data exists and for how long | [`data-inventory.md`](data-inventory.md) |

The legal texts are the exception: [`privacy.md`](privacy.md) and
[`terms.md`](terms.md) mirror the published web pages, whose bodies live in
`web/src/legal/`. If the two ever disagree, the published page is authoritative.
