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

- `severity`: `info`, `warning`, or `error`
- `include` and `exclude`: doublestar file patterns
- `kinds`: `function` or `type`
- `exceptions`: cases that should pass
- `localize`: `comment`, `field`, or `statement`; use `[]` to disable pointers

## How it works

- Functions and types are checked in parallel, four at a time by default.
- Applicable rules are batched into one request per code unit.
- Failed rules get a second pass to locate the relevant code.
- Results include syntax-highlighted snippets and pointers when available.
- Network failures and retryable API responses are retried up to twice.

Jevlint sends extracted source code and file metadata to TypeSafe.

## Supported languages

- JavaScript, JSX, TypeScript, and TSX
- Python
- Go
- Rust

## Limits

- Evaluation is function- or type-declaration-sized.
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
