# hex — swarm memory

Maintained by the hex skills. Small by contract: pointers and preferences, not
copies. Team-shared — commit it.

## Pointers

- Verification: `AGENTS.md` › "Verification" — `task test`, `task check` before a merge.
- Selective tests: `AGENTS.md` › "Verification" — where the selective test command is documented.
- Build jobs: `unset` — Go uses all cores; no number recorded.
- Hub files: `AGENTS.md` › "Verification" (generated files) — pipelines never commit them.
- Fresh gate: `GOFLAGS=-count=1 task check`.
- Spec / plan / ADR conventions: `AGENTS.md` › "Conventions".
- Spec home: `docs/specs/` — the fold-back target; default ID-marker heading shape.
- Product knowledge: `AGENTS.md` › "Product".
- Key rules: `AGENTS.md` › "Binding rules" — zero third-party dependencies, the `container` CLI
  as the only runtime interface, generic relations only, security-sensitive paths.
- Writing style: `AGENTS.md` › "Documentation and code"; commits: `AGENTS.md` › "Git".
- Worktrees: default `.agents/worktrees/` (gitignored; `.agents/` itself is tracked).
- Retro: `.agents/retro/` — inbox (gitignored), ledger + reports (committed); owner /hex-retro.

## Preferences

```yaml
# hex config, vocabulary v5. Unknown keys warn once and are ignored.
models:
  light: haiku
  standard: sonnet
  standard-high: sonnet
  deep: opus

perspectives:
  always:
    - role: reviewer:security
      when: "internal/gitlab/**"
    - role: reviewer:security
      when: "internal/container/**"
    - role: reviewer:security
      when: "cmd/**"
    - role: reviewer:security
      when: "Taskfile.yml"
    - role: reviewer:security
      when: "githooks/**"
    - role: reviewer:security
      when: "scripts/**"
```

- Security review here means: the job token in the stage script and in the container, the
  image allow-list, mounts and other `container` arguments built from job input, the runner's
  flags and environment, and the module proxy and host scripts of the build.
- Cross-model adversary: not pinned. `nox-review` is installed, but the `nox` binary is not on
  `PATH`; re-run `/hex-init` once it is.

## Memory
