# Contributing Guide

🌐 **English** | 歡迎貢獻本專案！

## Development Environment

### Prerequisites

- Go 1.24+
- GCC (for SQLite CGO compilation)

### Setup

```bash
git clone https://github.com/moehoshio/nginx-request-attribution.git
cd nginx-request-attribution
go mod download
```

## Development Workflow

### 1. Run Tests

All changes **must** pass tests before submitting:

```bash
go test ./...
```

Run with verbose output and race detection:

```bash
go test -v -race ./...
```

### 2. Build

```bash
go build -o nginx-req-attr ./cmd/
```

## Writing Tests

### Requirements

When developing new modules, you **must** include corresponding tests:

- Every new package under `internal/` must have at least one `_test.go` file.
- Test functions must follow Go naming conventions: `TestXxx(t *testing.T)`.
- Use table-driven tests where appropriate for better coverage and readability.
- Tests should cover both normal cases and error/edge cases.

### Test File Structure

Place test files alongside the source code in the same package:

```
internal/
├── parser/
│   ├── parser.go
│   ├── parser_test.go    ← Tests for parser
│   └── useragent.go
├── config/
│   ├── config.go
│   └── config_test.go    ← Tests required for new modules
└── ...
```

### Example: Table-Driven Test

```go
func TestMyFunction(t *testing.T) {
    tests := []struct {
        name     string
        input    string
        expected string
        wantErr  bool
    }{
        {
            name:     "valid input",
            input:    "hello",
            expected: "HELLO",
        },
        {
            name:    "empty input",
            input:   "",
            wantErr: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := MyFunction(tt.input)
            if (err != nil) != tt.wantErr {
                t.Errorf("MyFunction() error = %v, wantErr %v", err, tt.wantErr)
                return
            }
            if got != tt.expected {
                t.Errorf("MyFunction() = %v, want %v", got, tt.expected)
            }
        })
    }
}
```

## CI/CD

### Automated Testing

All pull requests will automatically run the CI workflow (`.github/workflows/ci.yml`) which:

1. Runs `go test -v -race ./...` to execute all tests with race detection
2. Runs `go build` to verify the project compiles

PRs must pass CI before merging.

### Copilot Integration

The project is configured with `.github/copilot-setup-steps.yml` to ensure Copilot runs all tests locally before submitting any changes. This prevents broken code from being committed.

## Code Style

- Follow standard Go conventions (`gofmt`).
- Keep functions focused and small.
- Use meaningful variable and function names.
- Add comments for exported functions and complex logic.

## Submitting Changes

1. Create a feature branch from `main`.
2. Make your changes with corresponding tests.
3. Ensure all tests pass: `go test ./...`
4. Ensure the build succeeds: `go build -o nginx-req-attr ./cmd/`
5. Submit a pull request.
