# Built-in provider packs

A pack gives Ataniq a stable name for a provider runtime and describes the builds for which it has evidence. Each pack embeds its own `declaration.json`. The `ataniq.pack-declaration/1` schema records the identity, boundary words and build rows. A row names an exact version, its transports, its evidence source and the date of verification when known. A version alias is another exact spelling for terminal lookup.

| Pack | Qargi runtime | Terminal version | Alias |
| --- | --- | --- | --- |
| `claude` | `claude-code` | `2.1.325` | |
| `codex` | `codex` | `0.169` | `0.169.0` |

## Adding a pack

1. Create `packs/<name>/declaration.json` and implement `pack.Pack` in `pack.go`. Decode the embedded bytes with `pack.DecodeDeclaration`. Its strict reader requires the file to end at the closing brace. Cite the fixture source for each build and leave `verified_on` empty when the date is unknown.
2. Add the pack to `all.Register` and test its identity, words and exact build rows. The command or qualification main creates a registry with `pack.New()`, registers its packs, handles any error and calls `Freeze()` before sharing it.
3. Keep each provider concern in its own file. A pack can use the standard library, the neutral pack and concern contracts, and shared code under `packs/internal`. Only the composition packages may import sibling packs.

Provider names, boundary words and provider-specific decisions belong in the packs. Core looks up packs by name and uses neutral interfaces. Code above the transports uses the session vocabulary. Core tests use the synthetic packs in `internal/pack/packtest`. Put tests that need real packs in the provider package or in `packs/all`.
