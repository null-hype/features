## Lights

An agent reports its run from inside its Herdr pane:

```sh
xeno verifier confirmed   # or failed
xeno falsifier failed     # or refuted
xeno jev 0.9              # Jev's confidence both arms were genuine
```

Herdr shows the lights only where your sidebar rows ask for them. Add the
tokens to `~/.config/herdr/config.toml`, in the order of a window's traffic
lights:

```toml
[ui.sidebar.agents]
rows = [
  ["state_icon", "workspace", "tab"],
  [
    "agent",
    { token = "$xeno_falsifier", fg = "#ff5f57" },
    { token = "$xeno_jev", fg = "#febc2e" },
    { token = "$xeno_verifier", fg = "#28c840" },
    { token = "$xeno_status", dim = true, rules = [
      { equals = "uncontrolled", fg = "#febc2e", dim = false },
      { equals = "unverified", fg = "#ff5f57", bold = true, dim = false },
    ] },
  ],
]
```

Red is the falsifier's slot and green the verifier's, not bad and good news: a
lit light means that arm ran. Only `uncontrolled` and `unverified` call for
attention; `refuted` is an on-target run whose prediction lost.

Without the Dev Container, install the plugin alone with
`herdr plugin install null-hype/features/src/xeno/plugin`.
