# Jevlint

Jevlint checks code against plain-language rules. It uses Tree-sitter to extract
code units, then asks Jev whether each one passes.

## Install

Download a prebuilt binary from
[Releases](https://github.com/codegirl-007/jevlint/releases) — `linux` and
`macos` on `amd64`/`arm64`, and `windows` on `amd64` — unpack it, and put
`jevlint` on your `PATH`.

Or install with Go:

```sh
go install github.com/codegirl-007/jevlint/cmd/jevlint@latest
```

## Getting started

```sh
cd your-project
jevlint init                          # write a starter jevlint.json
export TYPESAFE_API_KEY=apikey_...    # from console.typesafe.ai
jevlint doctor                        # verify the config and credentials
jevlint check .
```

`init` detects the languages in the project and writes a starter
`jevlint.json`. Add rules to `jevlint.json` (or install a pack) before the
first check.

## Requirements

Building from source (or installing with `go install`) needs:

- Go 1.26 or newer
- CGO enabled
- A C compiler

Running checks needs a TypeSafe API key from
<https://console.typesafe.ai/settings/keys>.

## Commands

| Command | Description |
| --- | --- |
| `jevlint init` | Write a starter `jevlint.json` for the project. |
| `jevlint doctor` | Check that the config loads and the credentials work. |
| `jevlint check [paths...]` | Check code against the rules. |
| `jevlint eval` | Score rules against fixtures in `jevlint-evals.json`. |
| `jevlint plugin ...` | Manage rule packs. |
| `jevlint version` | Print the version. |

`init` and `doctor` accept `--json` for machine-readable output.

## Run

Set `TYPESAFE_API_KEY` in your environment (see
[Configuration](#configuration)), then run from the project root:

```sh
go run ./cmd/jevlint init
go run ./cmd/jevlint doctor
go run ./cmd/jevlint check .
go run ./cmd/jevlint check --format json .
go run ./cmd/jevlint check --concurrency 8 . 
go run ./cmd/jevlint check --refresh-cache .
go run ./cmd/jevlint eval
go run ./cmd/jevlint eval --rule database-joins --format json
```

| Flag | Description |
| --- | --- |
| `--changed` | Check only git-modified files (staged, unstaged, and untracked). |
| `--clear-cache` | Clear this project's cached evaluations before checking. New results are cached. |
| `--color auto\|always\|never` | Control colored text output. Defaults to `auto`. |
| `--config path` | Use a different rule file. Its directory becomes the project root. |
| `--concurrency number` | Set the maximum number of concurrent Jev requests. Defaults to `4`. |
| `--format text\|json` | Select human-readable or machine-readable output. Defaults to `text`. |
| `--refresh-cache` | Reevaluate code and replace matching cached results. |

## Configuration

Jevlint reads `jevlint.json` for rules. Credentials and provider settings come
from environment variables.

```sh
export TYPESAFE_API_KEY=apikey_...
```

`init` writes only `jevlint.json`; it does not create or modify any other file.

- **Keep it secret:** export the key from your shell profile or a secret
  manager. Jevlint never writes credentials into the project.
- **Debugging:** set `JEVLINT_DEBUG=1` to print each request URL, the
  credential kind (never the value), the request payload (capped for size), and
  the response to stderr. Search it with `less`: redirect stderr to a file and
  look for `payload to jev`.

### Providers

Jevlint selects a provider from `JEVLINT_PROVIDER`: `typesafe`, `jev`,
`cloudflare`, `clef`, or `openrouter` (default: `typesafe`). The default talks
to TypeSafe's Jev at `https://api.typesafe.ai/v1/systemone`.
`cloudflare`/`clef` use Cloudflare Workers AI, and `openrouter` uses Jev
through OpenRouter (see below).

| Variable | Meaning |
| --- | --- |
| `JEVLINT_PROVIDER` | Provider: `typesafe`, `jev`, `cloudflare`, `clef`, or `openrouter` (default: `typesafe`). |
| `TYPESAFE_API_KEY` | API key. |
| `TYPESAFE_BASE_URL` | Service base URL. Defaults to `https://api.typesafe.ai`. |
| `TYPESAFE_DEFAULT_MODEL` | Model name. Defaults to `jev-latest`. |
| `TYPESAFE_ENDPOINT` | Full request URL, bypassing `TYPESAFE_BASE_URL`. |

Set `TYPESAFE_ENDPOINT` to target another SystemOne-compatible service.

#### Cloudflare Workers AI (Clef)

Jevlint can use the [Clef decision models on Cloudflare Workers
AI](https://developers.cloudflare.com/workers-ai/models/clef) instead of Jev.
Set `JEVLINT_PROVIDER=cloudflare`:

```sh
export JEVLINT_PROVIDER=cloudflare
export CLOUDFLARE_ACCOUNT_ID=<account id>
export CLOUDFLARE_AUTH_TOKEN=<cloudflare api token>
export CLEF_MODEL=clef        # or clef-flash
```

| Variable | Meaning |
| --- | --- |
| `CLOUDFLARE_ACCOUNT_ID` | Cloudflare account id. |
| `CLOUDFLARE_AUTH_TOKEN` | Cloudflare API token. `CLOUDFLARE_API_TOKEN` is accepted as an alias; if both are set, they must match. |
| `CLEF_MODEL` | `clef` (default) or `clef-flash`. |

Requests go to
`https://api.cloudflare.com/client/v4/accounts/<account id>/ai/run/@cf/cloudflare/<model>`.

Create a **Workers AI API Token** with `Workers AI: Read` and
`Workers AI: Edit` permissions:

1. Open the [Workers AI page](https://dash.cloudflare.com/?to=/:account/ai/workers-ai)
   and select **Use REST API**.
2. Select **Create a Workers AI API Token**, then copy it — it is shown only
   once.

You can also create one from the
[API Tokens page](https://dash.cloudflare.com/profile/api-tokens) using the
**Workers AI** template. The account id is on the Workers AI page, or in the
dashboard URL. A Global API Key, or a token without the Workers AI permission,
is rejected. See the
[Workers AI REST API guide](https://developers.cloudflare.com/workers-ai/get-started/rest-api/)
for details.

Clef has the same request and response shape as Jev, so rules, findings, and
caching work unchanged.

#### OpenRouter (Jev)

[OpenRouter](https://openrouter.ai) serves Jev through a System One endpoint, so
you can run Jevlint with an OpenRouter key and OpenRouter billing instead of a
TypeSafe account. Set `JEVLINT_PROVIDER=openrouter`:

```sh
export JEVLINT_PROVIDER=openrouter
export OPENROUTER_API_KEY=sk-or-...
export OPENROUTER_MODEL=typesafe/jev-1.13   # or ~typesafe/jev-latest
```

| Variable | Meaning |
| --- | --- |
| `OPENROUTER_API_KEY` | OpenRouter API key. |
| `OPENROUTER_MODEL` | Model id. Defaults to `typesafe/jev-1.13`. |
| `OPENROUTER_BASE_URL` | Service base URL. Defaults to `https://openrouter.ai/api`. |
| `OPENROUTER_SITE_URL` | Optional; sent as the `HTTP-Referer` attribution header. |

Requests go to `https://openrouter.ai/api/v1/systemone`, and the
`X-Title: jevlint` attribution header is sent. Create a key at
<https://openrouter.ai/settings/keys>. Jev is a decision model with the same
request and response shape as TypeSafe's Jev, so rules, findings, and caching
work unchanged.

## Rules

Jevlint reads `jevlint.json` by default.

```json
{
  "languages": {
    "go": {},
    "typescript": {},
    "tsx": {}
  },
  "rules": [
    {
      "id": "database-joins",
      "description": "Join related database records in the database.",
      "severity": "error",
      "kinds": ["function"],
      "include": ["src/**/*.go"],
      "exclude": ["**/*_test.go"],
      "exceptions": ["The records come from different databases."],
      "localize": ["statement"]
    }
  ]
}
```

No languages are enabled by default, and a config must enable at least one
language and define at least one rule (or list a pack). Available presets are
`c`, `cpp`, `csharp`, `go`, `java`, `javascript`, `kotlin`, `php`, `python`,
`ruby`, `rust`, `tsx`, and `typescript`.

Each preset includes common extensions, extraction queries, and localization
regions. Override only what your project needs:

```json
{
  "languages": {
    "cpp": {
      "extensions": [".cpp", ".hpp"]
    }
  }
}
```

Presets use native Tree-sitter grammars compiled into Jevlint. Config can
customize a preset, but it cannot load an arbitrary external grammar.

- `severity`: `info`, `warning`, or `error`
- `include` and `exclude`: doublestar file patterns
- `kinds`: `comment`, `docComment`, `field`, `function`, `statement`, or `type`
- `exceptions`: cases that should pass
- `localize`: `comment`, `docComment`, `field`, or `statement`. Omit the key
  or use `[]` to skip the second pass. Each matching region is another Jev
  request on a fail, up to 24 regions per function or type.
- `minConfidence`: optional `0`–`1`. Omit or `0` uses every Jev result. Failures
  below the minimum are not reported. A rule `minConfidence` overrides the
  global value when set.
- `allowSkip`: let Jev answer `skip` when the rule does not apply to the unit.
  Skip is not a finding.
- `allowAbstain`: let Jev answer `abstain` when the rule applies but there is
  not enough context to decide. Abstain is not a finding.
- `context`: deterministic repository evidence to include as extra state. The
  evidence is gathered from a repository-wide index built once per run, and
  every item carries file, line, and source provenance. Rules with the same
  context are batched together, but each distinct context is a separate request:
  a unit checked by rules with N different `context` settings is sent up to N
  times.
  - `context.callees`: functions this unit directly calls.
  - `context.callers`: functions that directly call this unit (one hop).
  - `context.relatedTypes`: directly related type declarations and, for a
    method, its containing type.
  - `context.imports`: imports the unit uses as a qualifier, in a call or a type
    position.

```json
{
  "id": "database-joins",
  "description": "Fail when related database records are joined in application code instead of in the query.",
  "kinds": ["function"],
  "context": {
    "callees": true
  }
}
```

Context is conservative: when a relationship cannot be resolved, it is omitted
rather than guessed. Calls resolve to a same-file unique function, a
project-unique function, or a function in a package named by an unambiguous
import alias. Ambiguous names, receiver-typed calls, external packages, and
generic references are left out. The `relatedTypes` behavior is opt-in, so add
`"relatedTypes": true` to keep the type context earlier versions always sent.

Evidence is sent to the service in the code unit's `evidence` field and is also
written onto findings under `evidence`, so an output consumer can see exactly
what repository context Jevlint supplied without asking Jev to produce
provenance. Every relationship is one `evidence` item tagged with its `kind`.

## How it works

- Rules can check functions, types, comments, fields, or statements.
- Comments, fields, and statements include their nearest function or type as context.
- Code units are checked in parallel, four at a time by default.
- Applicable rules with the same context requirements are batched into one
  request per code unit.
- Failed function and type rules can set `localize` for a focused second pass.
  That pass is extra Jev evaluations and is off unless the rule lists
  categories.
- Results include syntax-highlighted snippets and pointers when available.
- Network failures and retryable API responses are retried up to twice.

Jevlint sends extracted source code and file metadata to TypeSafe.

## Cache

Jevlint caches validated results in the operating system's user cache directory.
Entries are isolated by project and contain results only, never source code or
API keys.

The cache key includes the API endpoint, model, API credential fingerprint, and
the exact request sent to Jev. Code, context, rules, prompts, or batch changes
create a new entry. Severity changes reuse the result because severity only
affects reporting.

Entries do not expire automatically. Because `jev-latest` can change without
changing its name, use `--refresh-cache` to reevaluate and replace cached
results. Use `--clear-cache` to clear this project's cache before a run.

## Eval

`jevlint eval` scores your rules against fixtures you list in
`jevlint-evals.json` (next to `--config`, or `--evals`). Check never loads that
file. Each case names a rule, a fixture relative to the eval file, and
`expect: pass` or `expect: fail`. Pack evals stay with the pack unless you
pass `--packs`.

Eval evaluates only that rule. It clears the rule's include and exclude so
fixtures still run, and keeps kinds, exceptions, localize, and minConfidence.
A case must evaluate at least one applicable code unit or eval exits `2`.

The repository ships its own cases in `jevlint-evals.json` covering the
fixtures under `examples/rules`. Folder names such as `good` and `bad` are
organizational only; the case's `expect` value decides the outcome. A rule can
have many cases, including several for the same language.

`examples/context-evals/` is a separate area for measuring one rule across
`context` variants. It keeps its own `jevlint-evals.json` (next to the variant
configs, since `--evals` defaults to the `--config` directory) and points at
fixtures under `examples/rules/function-name-behavior-mismatch/context/`. Run it
with `examples/context-evals/run.sh` or, for one variant,
`jevlint eval --config examples/context-evals/callees.json`.

```json
{
  "version": 1,
  "cases": [
    {
      "rule": "database-joins",
      "file": "examples/rules/database-joins/bad/example.go",
      "expect": "fail"
    }
  ]
}
```

| Flag | Description |
| --- | --- |
| `--clear-cache` | Clear this project's cached evaluations before evaluating. |
| `--color auto\|always\|never` | Control colored text output. Defaults to `auto`. |
| `--config path` | Use a different rule file. Its directory becomes the project root. |
| `--concurrency number` | Set the maximum number of concurrent Jev requests. Defaults to `4`. |
| `--evals path` | Use a different eval file. Fixtures stay relative to that file. |
| `--format text\|json` | Select human-readable or machine-readable output. Defaults to `text`. |
| `--packs` | Also run evals from packs listed in `jevlint.json`. |
| `--refresh-cache` | Reevaluate code and replace matching cached results. |
| `--rule id` | Evaluate only this rule's cases. |
| `--verbose` | Include the per-unit decisions in JSON output. |

Each case evaluates the rule against every code unit in the fixture. Each
evaluation returns one raw decision: `pass`, `fail`, `skip`, or `abstain`. The
tool reports a violation only when Jev's confidence reaches the rule's
confidence floor (the rule's `minConfidence`, or the global one); each reported
violation becomes a finding.

The case outcome follows from those decisions:

- `fail`: at least one violation was reported.
- `inconclusive`: no violation was reported, but Jev flagged one below the
  confidence floor, or no unit returned an explicit pass (only `skip` or
  `abstain`).
- `pass`: otherwise, meaning at least one explicit pass and no failure.

A case matches when its outcome equals `expect`, and `inconclusive` never
matches. This keeps an expected pass from succeeding just because evaluations
skipped, abstained, or hid a failure under the confidence floor, and it makes
an expected fail require a reportable violation.

Text output streams as Jev answers come back. It starts with a legend, prints
each code unit's answer and confidence as it arrives, then prints the case's
expected and actual outcome. The run ends with
`N/M eval cases matched expectations, K inconclusive`. Cases stay in file
order; units within a case print in completion order.

JSON is written once after the run finishes. It includes the per-case
confidence, the raw decision counts (`pass`, `fail`, `skip`, `abstain`,
`reported`, `belowFloor`), the confidence floor, and the suite totals
(`reportedFailures`, `belowFloorFailures`). `--verbose` adds a `units` array
with each code unit's kind, name, lines, status, confidence, and whether its
failure was reported.

Exit codes:

- `0`: every case matched
- `1`: at least one case was mismatched or inconclusive
- `2`: configuration, parsing, or provider error

## Packs

A pack is a shared directory with a `pack.json` manifest, rules, optional evals,
and fixtures. Pins live in `jevlint.json`; fetched files live in the user cache,
not the project tree.

```text
pack.json
rules.json
jevlint-evals.json
fixtures/
```

```json
{
  "version": 1,
  "id": "codegirl-007/database-joins",
  "languages": ["go"],
  "rules": "rules.json",
  "evals": "jevlint-evals.json"
}
```

`rules` and `evals` are optional and default to `rules.json` and
`jevlint-evals.json`; both must stay inside the pack. Any languages a pack
declares must be enabled in your config. Pack ids are `owner/name`, where each
part is a simple identifier (letters, digits, `.`, `_`, `-`), and packs
containing symbolic links are rejected.

```json
{
  "languages": { "go": {} },
  "minConfidence": 0.8,
  "packs": [
    {
      "id": "codegirl-007/database-joins",
      "source": "https://github.com/codegirl-007/jevlint.git",
      "path": "examples/packs/database-joins",
      "sha": "<commit-sha>"
    }
  ],
  "rules": [
    {
      "id": "database-joins",
      "minConfidence": 0.5,
      "include": ["src/**/*.go"]
    }
  ]
}
```

Create a new pack with `plugin init`:

```sh
jevlint plugin init codegirl-007/database-joins
jevlint plugin init my-org/my-pack --languages go,typescript --dir ./my-pack
```

It writes `pack.json`, `rules.json`, and a README, plus example evals and Go
fixtures when `go` is one of the languages. Commit the result to a git
repository to install it.

`plugin install` writes the pin. A project can use only pack rules. Project
`rules` entries overlay a pack rule by id (confidence, include/exclude,
severity) or add a local rule. Check never opens pack evals.

The repository ships a sample pack at `examples/packs/database-joins`. Install
it with a GitHub tree URL, or install your own pack from a tree URL or a local
git repository:

```sh
go run ./cmd/jevlint plugin install https://github.com/codegirl-007/jevlint/tree/master/examples/packs/database-joins
go run ./cmd/jevlint plugin install /path/to/packs#database-joins
go run ./cmd/jevlint plugin list
go run ./cmd/jevlint plugin update
go run ./cmd/jevlint plugin remove codegirl-007/database-joins
```

## Supported languages

| Preset | Extensions |
| --- | --- |
| `c` | `.c` |
| `cpp` | `.cc`, `.cpp`, `.cxx`, `.h`, `.hpp`, `.hxx` |
| `csharp` | `.cs` |
| `go` | `.go` |
| `java` | `.java` |
| `javascript` | `.js`, `.jsx`, `.mjs`, `.cjs` |
| `kotlin` | `.kt`, `.kts` |
| `php` | `.php`, `.phtml` |
| `python` | `.py` |
| `ruby` | `.rb`, `.rake`, `.gemspec` |
| `rust` | `.rs` |
| `tsx` | `.tsx` |
| `typescript` | `.ts`, `.mts`, `.cts` |

## Limits

- Evaluation is scoped to the configured code-unit kinds.
- Comments, fields, and statements receive only their nearest declaration as parent context.
- Jevlint can optionally include bounded depth-1 project-local callee context,
  but it does not perform recursive call-graph or cross-function data-flow
  analysis. Imports are not followed.
- Type context is limited to the same file.
- Jev returns a constrained choice, not a free-form explanation.

## Exit codes

- `0`: no findings
- `1`: findings remain
- `2`: configuration or runtime error
