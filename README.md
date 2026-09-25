# Jevlint

Jevlint checks code against plain-language rules. It uses Tree-sitter to extract
functions and types, then asks Jev whether each one passes.

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
```

Flags must appear before source paths.

```sh
go build -o jevlint ./cmd/jevlint
```

Use `--color always` or `--color never` to control colored output. Set
`TYPESAFE_BASE_URL` or `TYPESAFE_DEFAULT_MODEL` to override the API defaults.

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
- `localize`: `comment`, `field`, or `statement`; use `[]` to disable pointers

## How it works

- Rules can check functions, types, comments, fields, or statements.
- Comments, fields, and statements include their nearest function or type as context.
- Code units are checked in parallel, four at a time by default.
- Applicable rules are batched into one request per code unit.
- Failed function and type rules can use `localize` for a focused second pass.
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
changing its name, use `--refresh-cache` for fresh model behavior. Use
`--no-cache` to bypass caching or `--clear-cache` to clear this project's cache
before a run.

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
- There is no autofix.

## Exit codes

- `0`: no findings
- `1`: findings
- `2`: configuration or runtime error

## Verify

```sh
go test -race ./...
go vet ./...
go build ./cmd/jevlint
```
