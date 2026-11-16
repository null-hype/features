## Lights

An agent reports its run from inside its Herdr pane:

```sh
xeno verifier confirmed   # or failed
xeno falsifier failed     # or refuted
xeno jev 0.9              # Jev's confidence both arms were genuine
```

Herdr shows the lights only where your sidebar rows ask for them. Append
[`sidebar.toml`](sidebar.toml) to `~/.config/herdr/config.toml` and run
`herdr server reload-config`. It puts the falsifier, Jev and verifier lights,
in a window's traffic-light order, after each agent's name, followed by
`$xeno_status` and the reason verdict `$xeno_reason`.

Red is the falsifier's slot and green the verifier's, not bad and good news: a
lit light means that arm ran. Only `uncontrolled` and `unverified` call for
attention; `refuted` is an on-target run whose prediction lost.

## Diagnostics

Name where the claim lives and xeno also writes a
[witness](https://github.com/null-hype/witness) run, so the verdict shows
in the editor on the claim's line, with each arm's evidence a go-to-definition
away:

```sh
xeno claim dagger/delivery.go:33
xeno verifier confirmed verifier.diff   # e.g. the arm's container-use diff
xeno falsifier failed falsifier.diff
xeno jev 0.9
```

`uncontrolled` is a warning, `unverified` an error, and an on-target run,
refuted or not, information. The [recorded example](https://github.com/null-hype/features/tree/a3d9c20cba65f374d395056fb4a6a467a804a14e/src/xeno/example) replays the
`people-can-install-witness` false positive from recorded diffs: green as a
check, a warning to xeno with one arm, an error once the falsifier runs and
Jev doubts the verifier installed witness the way people do, and information
once it does.

Without the Dev Container, install the plugin alone with
`herdr plugin install null-hype/features/src/xeno`.

## Reason assessment

The collector branch also includes this reporter. `xeno assess` uses the
reason-label scorer to feed it a reason verdict and Witness finding, while
the original verifier/falsifier/jev commands retain their control semantics.
See the repository README for manual use, the Herdr action and Claude Stop hook.

The recorded live Jev calls in `test/xeno/fixtures/reason-judgments.json`
retain the successful classification and redaction controls, the crossed
reason, and a deliberately wrong classification reply. The wrong reply was
predicted to yield `unfulfilled`; Jev also lowered the reason-fit score, so
it yielded `uncertain`. This missed prediction stays in the capture. The
replay test checks transport and recorded verdicts without another API call.

## Reasons × threads

For one Herdr session, select its completed Claude JSONL traces explicitly:

```sh
xeno heatmap \
  --positive 'Verify that Claude distinguishes healthy and failed HTTP probes.' \
  --negative 'Deploy a production Kubernetes cluster.' \
  --output .witness/run/evidence \
  /path/to/session-thread-a.jsonl /path/to/session-thread-b.jsonl
```

The command refreshes the signed-in Pass agent's audit and reuses the existing
reason assessor for each whole thread. `--reasons /path/to/reasons.json` selects
a retained `pass-cli agent monitor --output json` export instead. Supply a resolved
`TYPESAFE_API_KEY`, as for `assess`. Use completed traces: Stop hooks remain the
single-thread assessment path.

Open `reason-heatmap.html` in the output directory. Rows are distinct ItemRead
reasons plus the marked positive and negative controls. Columns are the selected
threads. Color represents reason fit; the separate achievement score is also
visible. The positive control expects at least one fit ≥ 0.8; the negative
expects every fit ≤ 0.2. `missed` preserves a failed expectation. API failures
produce `unverified` cells and a nonzero exit, while retaining available results.

The adjacent private `reason-heatmap.json` retains exact requests/responses,
trace paths and hashes, and the audit hash. Control expectations are not sent
as model state. A thread may also be an OpenRouter agent's session file from the
agent-plugins toolbox trace sink (`GET /openrouter/traces/<session>`, one OTLP
payload per line). Its model calls, prompt and completion, are the thread, in
start order. This command does not infer thread membership or project
completion, change Herdr pane verdicts, or publish anything.
