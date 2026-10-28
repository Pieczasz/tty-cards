# Deferred design work

Held here, not as `TODO` comments (`godox` fails the lint). Delete an entry in the same change that does the work.

| What | Trigger |
|---|---|
| Split domain types from the GORM models | A second store, or a TUI bug from a missed `Preload` |
| Move presence and the session lifecycle out of `internal/ssh` | A second transport |
| A template-method framework for game views | A sixth game whose view is mostly hearts/gin plumbing |
| Split `internal/game` into an SPI package and the engine | Rules packages start needing engine internals, or a second engine |
| `Engine[S]` generics, or a typed `Extra` adapter | A sixth game |
