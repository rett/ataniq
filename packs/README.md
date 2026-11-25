# Built-in provider packs

A pack gives Ataniq a stable name for a provider runtime and describes the builds for which it has evidence. Each pack embeds its own `declaration.json`. The `ataniq.pack-declaration/1` schema records the identity, boundary words and build rows. A row names an exact version, its transports, its evidence source and the date of verification when known. A version alias is another exact spelling for terminal lookup.

| Pack | Qargi runtime | Terminal version | Alias |
| --- | --- | --- | --- |
| `claude` | `claude-code` | `2.1.325` | |
| `claude` | `claude-code` | `2.1.332` | |
| `codex` | `codex` | `0.169` | `0.169.0` |
| `codex` | `codex` | `0.172.0` | |

The screen-qualified Claude 2.1.325 and Codex 0.169 builds have no launch policy. A launch for either build, including the Codex alias, is refused with `policy-missing`. The newer rows identify installed builds by their captured help. Their verification dates remain empty.

## Adding a pack

1. Create `packs/<name>/declaration.json` and implement `pack.Pack` in `pack.go`. Decode the embedded bytes with `pack.DecodeDeclaration`. Its strict reader requires the file to end at the closing brace. Cite the fixture source for each build and leave `verified_on` empty when the date is unknown.
2. Add the pack to `all.Register` and test its identity, words and exact build rows. The command or qualification main creates a registry with `pack.New()`, registers its packs, handles any error and calls `Freeze()` before sharing it.
3. For a terminal build, add a `holder.Part` under the provider and register it in `packs/holder`. Its manifest contains only the pack name and the terminal build rows. The list imports `internal/pack` and the providers' holder subpackages. This lets the holder link just the parts it reads. The tests in `packs/all` compare both registries, including aliases and evidence sources.
4. Keep each provider concern in its own file. A pack can use the standard library, the neutral pack and concern contracts, and shared code under `packs/internal`. Only the composition packages may import sibling packs.

Provider names, boundary words and provider-specific decisions belong in the packs. Core looks up packs by name and uses neutral interfaces. Code above the transports uses the session vocabulary. Core tests use the synthetic packs in `internal/pack/packtest`. Put tests that need real packs in the provider package or in `packs/all`.

## Launch policies

The holder looks up a policy by pack and resolved build version. For the installed builds it reads `launchpolicy/<version>.json` and the adjacent help capture. Codex has one fixture containing both the main and exec help, with a labelled separator. The exec scope inventories options. The Codex terminal requires the fixed `--no-daemon` flag to isolate it from a running shared server.

Both policies ship with an empty fixture hash and pending terminal vectors. The matcher returns `policy-unrecorded` until the attended install records the fixture and resolves pending evidence. Recording the fixture alone cannot qualify a mode. The help for Claude names six modes without specifying their behavior, so all six have class `bypass`. The Codex requirements file permits only `on-request`, which delegates the approval decision to the model (class `bypass`). The launch policy therefore excludes `never` even for a coordinator. Both builds currently lack a lead-usable mode.

The Claude capture shows `reviewer` as a custom definition. Selecting it requires a reviewed definition in a required read-only agent overlay and a matching installed file in the renderer inventory.

Claude managed settings must constrain the provider version to the exact build. The build capture and delta §6.1 give no key or encoding for that range. The Claude policy therefore withholds the `provider-policy` file rule. Even with synthetic recording, the required settings file receives `file-refused`.
