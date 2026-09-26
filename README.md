# Jevlint

Jevlint checks code against plain-language rules. It uses Tree-sitter to extract
code units, then asks Jev whether each one passes.

## Requirements

- Go 1.26 or newer
- CGO enabled
- A C compiler
- A TypeSafe API key from <https://console.typesafe.ai/settings/keys>

## Run

```sh
export TYPESAFE_API_KEY="sk-..."

go run ./cmd/jevlint check .
go run ./cmd/jevlint check --format json src
go run ./cmd/jevlint check --concurrency 8 src
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

```sh
go build -o jevlint ./cmd/jevlint
```

Set `TYPESAFE_BASE_URL` or `TYPESAFE_DEFAULT_MODEL` to override the API defaults.

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

No languages are enabled by default. Available presets are `c`, `cpp`, `csharp`,
`go`, `java`, `javascript`, `kotlin`, `php`, `python`, `ruby`, `rust`, `tsx`,
and `typescript`.

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
- `kinds`: `comment`, `field`, `function`, `statement`, or `type`
- `exceptions`: cases that should pass
- `localize`: `comment`, `field`, or `statement`. Omit the key or use `[]` to
  skip the second pass. Each matching region is another Jev request on a fail,
  up to 24 regions per function or type.
- `minConfidence`: optional `0`–`1`. Omit or `0` uses every Jev result. Failures
  below the minimum are not reported.

## How it works

- Rules can check functions, types, comments, fields, or statements.
- Comments, fields, and statements include their nearest function or type as context.
- Code units are checked in parallel, four at a time by default.
- Applicable rules are batched into one request per code unit.
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
`expect: pass` or `expect: fail`.

Eval evaluates only that rule. It clears the rule's include and exclude so
fixtures still run, and keeps kinds, exceptions, localize, and minConfidence.
A case must evaluate at least one applicable code unit or eval exits `2`.

```json
{
  "version": 1,
  "cases": [
    {
      "name": "join-in-code",
      "rule": "database-joins",
      "file": "fixtures/orm.go",
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
| `--refresh-cache` | Reevaluate code and replace matching cached results. |
| `--rule id` | Evaluate only this rule's cases. |

Text output is grouped by rule and ends with `N/M eval cases matched expectations`.
JSON includes per-case confidence when a reportable fail is available.

- `0`: every case matched
- `1`: at least one case missed its expectation
- `2`: configuration or runtime error

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
- Imports and call graphs are not followed.
- Type context is limited to the same file.
- Database provenance and cross-function data flow are not traced.
- Jev returns a constrained choice, not a free-form explanation.

## Exit codes

- `0`: no findings
- `1`: findings remain
- `2`: configuration or runtime error

## Verify

```sh
go test -race ./...
go vet ./...
go build ./cmd/jevlint
```
