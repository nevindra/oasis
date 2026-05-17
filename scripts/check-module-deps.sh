#!/usr/bin/env bash
# check-module-deps.sh — verify the microkernel dependency invariants.
#
# Run from the repo root. Returns non-zero if any module imports something
# it shouldn't.
#
# Invariants:
#   1. The kernel (root module github.com/nevindra/oasis) must not import
#      any satellite module (anything under github.com/nevindra/oasis/<name>).
#      During the migration, "root" is the kernel because oasis/core hasn't
#      been split out yet. Once it has, update KERNEL_PATH below.
#   2. Each satellite module may only import the kernel plus dependencies
#      explicitly listed in its DECLARED_DEPS line below.
#
# As new modules are added by future extractions, add a DECLARED_DEPS entry
# for them.

set -euo pipefail

REPO="github.com/nevindra/oasis"
KERNEL_PATH="$REPO"  # update to "$REPO/core" after the core/ move

fail=0

check_kernel() {
    echo "==> Kernel discipline: $KERNEL_PATH imports nothing under $REPO/*"
    local violations
    violations=$(go list -deps "$KERNEL_PATH/..." 2>/dev/null \
        | grep -E "^$REPO/" \
        | grep -v "^$KERNEL_PATH$" \
        | grep -v "^$KERNEL_PATH/" || true)
    if [ -n "$violations" ]; then
        echo "  FAIL — kernel imports satellite modules:"
        echo "$violations" | sed 's/^/    /'
        fail=1
    else
        echo "  OK"
    fi
}

# check_satellite <module_path> <space-separated allowed extra deps under $REPO>
check_satellite() {
    local module="$1"
    shift
    local extra_allowed=("$@")
    echo "==> Module independence: $REPO/$module imports only kernel + declared deps"

    # Build regex of allowed prefixes: kernel + each extra dep
    local allowed_re="^$KERNEL_PATH(/|$)"
    for dep in "${extra_allowed[@]}"; do
        allowed_re="$allowed_re|^$REPO/$dep(/|$)"
    done

    local violations
    violations=$(cd "$module" && go list -deps ./... 2>/dev/null \
        | grep -E "^$REPO/" \
        | grep -Ev "$allowed_re" \
        | grep -v "^$REPO/$module$" \
        | grep -v "^$REPO/$module/" || true)
    if [ -n "$violations" ]; then
        echo "  FAIL — $module imports undeclared modules:"
        echo "$violations" | sed 's/^/    /'
        fail=1
    else
        echo "  OK"
    fi
}

check_kernel

# === Satellite modules (extend this list as extractions land) ===
check_satellite ratelimit
# check_satellite catalog
# check_satellite network
check_satellite guardrail
check_satellite compaction
# check_satellite mcp
# check_satellite workflow
# check_satellite rag

exit $fail
