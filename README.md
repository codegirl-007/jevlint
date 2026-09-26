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
go run ./cmd/jevlint fix .
go run ./cmd/jevlint check --fix .
```

| Flag | Description |
| --- | --- |
| `--clear-cache` | Clear this project's cached evaluations before checking. New results are cached. |
| `--color auto\|always\|never` | Control colored text output. Defaults to `auto`. |
| `--config path` | Use a different rule file. Its directory becomes the project root. |
| `--concurrency number` | Set the maximum number of concurrent Jev requests. Defaults to `4`. |
| `--format text\|json` | Select human-readable or machine-readable output. Defaults to `text`. |
| `--no-cache` | Bypass cache reads and writes for this run. |
| `--refresh-cache` | Reevaluate code and replace matching cached results. |
| `--fix` | Print current findings, then apply a validated proposal. Bypasses the cache. |

```sh
go build -o jevlint ./cmd/jevlint
```

Set `TYPESAFE_BASE_URL` or `TYPESAFE_DEFAULT_MODEL` to override the API defaults.

## Rules

Jevlint reads `jevlint.json` by default.

```json
{
  "fix": {
    "command": ["gemini", "--acp"],
    "exclude": ["examples/**"]
  },
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

## Autofix

`jevlint fix` asks a pre-authenticated ACP agent to fix current findings and
writes a validated proposal to the project. `jevlint check --fix` prints the
findings first, then does the same. Writes happen only after Jev validation
succeeds and the files on disk still match the snapshot used to generate the
proposal. Rejected proposals are never written.
The session attaches a `jevlint_check` MCP tool. The agent is told to call
that tool, not the `jevlint` CLI, and not finish until the check reports no
findings. Progress is written to stderr while the final diff or JSON is
written to stdout.

Configure any ACP agent command:

```json
{
  "fix": {
    "command": ["claude-agent-acp"]
  }
}
```

By default, the temporary workspace contains the safe, non-ignored project
tree. Use `fix.context` to narrow that read-only context and `fix.exclude` for
additional project-specific exclusions:

```json
{
  "fix": {
    "command": ["agent", "acp"],
    "context": ["src/**", "tests/**", "go.mod", "go.sum"],
    "exclude": ["src/generated/**"]
  }
}
```

Common choices are
[Claude Agent ACP](https://github.com/agentclientprotocol/claude-agent-acp),
[Codex ACP](https://github.com/agentclientprotocol/codex-acp), and Gemini CLI
with `["gemini", "--acp"]`.

Jevlint mirrors regular project files into a temporary directory, respecting
nested `.gitignore` files. It always excludes version-control metadata,
dependency and build directories, symlinks, special files, and common secret
files such as `.env`, private keys, and package-manager credentials. Finding
files and `jevlint.json` remain available when `fix.context` narrows the
snapshot.

The session includes a `jevlint_check` MCP tool so the agent can re-run Jevlint
on the snapshot. It must keep fixing until that check reports no findings.
The tool only checks; it cannot apply fixes. Mid-session checks use the project
evaluation cache so unchanged units are not sent to Jev again. The opening
`--fix` check and the final validation stay uncached. Temporary
`jevlint-fix-*` workspaces are deleted after the agent process tree exits.
Terminals stay disabled.

The agent can read and edit mirrored files. After the session, Jevlint rejects
created, deleted, or replaced files. Proposed source must parse and pass Jev
validation before its diff is shown. Candidate evaluations are not cached. The
ACP client does not provide terminal access, though the configured agent
executable may have its own local tools for targeted formatting and tests.

The configured agent is an external process and may send mirrored source to its
model provider. The temporary workspace is not an operating-system sandbox; the
command still runs as your user. Use only agents and providers you trust.

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
before a run. `--fix` also bypasses the cache.

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
- Autofix cannot create, delete, or rename files.
- `fix` and `--fix` write only validated edits to existing snapshot files.

## Exit codes

- `0`: no findings, or a validated fix was applied
- `1`: findings remain, or a proposed fix was rejected
- `2`: configuration or runtime error

## Verify

```sh
go test -race ./...
go vet ./...
go build ./cmd/jevlint
```
