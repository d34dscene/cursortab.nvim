build:
    cd server && go build

test *test_cases:
    cd server && go test ./... {{ if test_cases == "" { "" } else { "-run " + replace(test_cases, " ", "|") } }}
    cd server && go run ./eval/cmd/cursortab-eval run --scenarios eval/scenarios --baseline eval/baseline.json --check

test-e2e *test_cases:
    cd server && go test ./text/... -run 'TestE2E{{ if test_cases == "" { "" } else { "/(" + replace(test_cases, " ", "|") + ")" } }}' -v

update-e2e *test_cases:
    cd server && go test ./text/... -run TestE2E {{ if test_cases == "" { "-update" } else { "$(for c in " + test_cases + "; do printf ' -update-only %s' \"$c\"; done)" } }}

verify-e2e *test_cases:
    cd server && go test ./text/... -run TestE2E {{ if test_cases == "" { "-verify-all" } else { "$(for c in " + test_cases + "; do printf ' -verify %s' \"$c\"; done)" } }}

fmt:
    cd server && gofmt -w .

lint:
    cd server && deadcode -test ./...

# Run the eval harness. Replays cassettes offline, prints quality report,
# and updates eval/baseline.json. Exits 1 on regression.
eval *args:
    cd server && go run ./eval/cmd/cursortab-eval run --scenarios eval/scenarios --baseline eval/baseline.json {{args}}

# Check eval metrics against baseline without updating (for CI).
eval-check *args:
    cd server && go run ./eval/cmd/cursortab-eval run --scenarios eval/scenarios --baseline eval/baseline.json --check {{args}}

# Record missing cassettes for a target against the eval server.
# Requires CURSORTAB_EVAL_URL (default http://localhost:8000) and
# CURSORTAB_EVAL_API_KEY when the server needs one. Target name is the scenario's
# `targets:` identifier (matches cassette/<target>.ndjson). Examples:
#   just eval-record mellum-4b
#   just eval-record zeta-2.1 --scenario python-inheritance-to-composition
eval-record target *args:
    cd server && go run ./eval/cmd/cursortab-eval record --scenarios eval/scenarios --target {{target}} --missing {{args}}

# Re-record a single scenario/target pair (for model upgrades).
eval-rerecord target scenario *args:
    cd server && go run ./eval/cmd/cursortab-eval record --scenarios eval/scenarios --target {{target}} --scenario {{scenario}} {{args}}

# Dry-run the record flow without hitting the network, validates scenarios parse.
eval-dry-record target *args:
    cd server && go run ./eval/cmd/cursortab-eval record --scenarios eval/scenarios --target {{target}} --dry-run --api-key ignored {{args}}

# Run the harness unit + integration tests (includes the record→replay end-to-end loop).
eval-test *test_cases:
    cd server && go test ./eval/... {{ if test_cases == "" { "" } else { "-run " + replace(test_cases, " ", "|") } }}
