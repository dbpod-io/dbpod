#!/usr/bin/env bash
# install-and-run matrix test: install and run the lowest & highest
# installable version of every MySQL release series, then clean up.
#
# Usage (from the repository root):
#   bash testdata/install_run_matrix.sh [series ...]
#
# Arguments:
#   series ...                only test these series (e.g. 8.0 9.7);
#                             no arguments means test all series
#
# Environment:
#   DRY_RUN=1                 only print the plan, install/run nothing
#
# Notes:
#   - always runs via `go run main.go` so the latest code is exercised
#   - binaries are removed after each series to save disk space
#   - written for bash 3.2 (macOS default): no associative arrays
set -uo pipefail

DBPOD=(go run main.go)
DATA_ROOT=""
RUNNING_INSTANCES=()
FAILED_STEPS=()
SERIES_ARGS="${*:-}"
DRY_RUN="${DRY_RUN:-}"
PAIRS_FILE=$(mktemp /tmp/dbpod-matrix-pairs.XXXXXX)

log() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*"; }

cleanup_running() {
    for name in "${RUNNING_INSTANCES[@]:-}"; do
        [ -z "$name" ] && continue
        log "CLEANUP: stopping and removing instance $name"
        "${DBPOD[@]}" kill "$name" >/dev/null 2>&1 || true
        "${DBPOD[@]}" rm -f "$name" >/dev/null 2>&1 || true
    done
    [ -n "$DATA_ROOT" ] && rm -rf "$DATA_ROOT"
    rm -f "$PAIRS_FILE"
}
trap cleanup_running EXIT

# --- step 1: collect installable versions per series -----------------------
log "STEP 1: listing all versions and filtering installable ones"
if ! LS_OUTPUT=$("${DBPOD[@]}" engine ls --all); then
    log "FATAL: engine ls --all failed"
    exit 1
fi

SKIPPED_UNAVAILABLE=0
SKIPPED_INSTALLED=0

while IFS= read -r line; do
    tokens=($line)
    [ "${#tokens[@]}" -lt 2 ] && continue
    [ "${tokens[0]}" != "mysql" ] && continue
    version="${tokens[1]}"
    [[ "$version" =~ ^[0-9]+\.[0-9]+ ]] || continue

    lts=""
    status=""
    for ((i = 2; i < ${#tokens[@]}; i++)); do
        case "${tokens[$i]}" in
        yes) lts="yes" ;;
        installed | unavailable) status="${tokens[$i]}" ;;
        esac
    done

    if [ "$status" == "unavailable" ]; then
        SKIPPED_UNAVAILABLE=$((SKIPPED_UNAVAILABLE + 1))
        continue # no package for the current platform
    fi
    # status == installed still counts: `engine install` is idempotent

    major="${version%%.*}"
    rest="${version#*.}"
    minor="${rest%%.*}"
    if [ "$major" -ge 10 ] && [ "$lts" != "yes" ]; then
        series="innovation"
    else
        series="$major.$minor"
    fi
    echo "$series $version" >> "$PAIRS_FILE"
done <<< "$LS_OUTPUT"

log "filtered: unavailable=$SKIPPED_UNAVAILABLE (installed versions stay in the plan)"

# --- step 2: plan min/max installable version per series --------------------
plan_series() { awk '{print $1}' "$PAIRS_FILE" | sort -u; }
min_of() { awk -v s="$1" '$1 == s {print $2}' "$PAIRS_FILE" | sort -V | head -1; }
max_of() { awk -v s="$1" '$1 == s {print $2}' "$PAIRS_FILE" | sort -V | tail -1; }
count_of() { awk -v s="$1" '$1 == s' "$PAIRS_FILE" | wc -l | tr -d ' '; }

PLAN_SERIES=$(plan_series)
if [ -z "$PLAN_SERIES" ]; then
    log "FATAL: no installable series found"
    exit 1
fi

# apply the series filter at plan time so the plan matches the execution
if [ -n "$SERIES_ARGS" ]; then
    FILTERED=""
    EXCLUDED=0
    for series in $PLAN_SERIES; do
        if [[ " $SERIES_ARGS " == *" $series "* ]]; then
            FILTERED="$FILTERED$series"$'\n'
        else
            EXCLUDED=$((EXCLUDED + 1))
        fi
    done
    PLAN_SERIES=$(echo "$FILTERED" | sed '/^$/d')
    log "series filter '$SERIES_ARGS': $EXCLUDED series excluded"
    if [ -z "$PLAN_SERIES" ]; then
        log "FATAL: no series left after applying the filter"
        exit 1
    fi
fi

PLAN_COUNT=$(echo "$PLAN_SERIES" | wc -l | tr -d ' ')
log "STEP 2: plan ready — $PLAN_COUNT series to test"
for series in $(echo "$PLAN_SERIES" | sort -V); do
    log "  series $series: min=$(min_of "$series") max=$(max_of "$series") ($(count_of "$series") installable)"
done

if [ -n "$DRY_RUN" ]; then
    log "DRY_RUN=1 — plan only, exiting"
    exit 0
fi

# --- step 3: install + run min/max of each series ---------------------------
DATA_ROOT=$(mktemp -d /tmp/dbpod-matrix.XXXXXX)

remove_from_running() {
    local name="$1"
    local kept=()
    for n in "${RUNNING_INSTANCES[@]:-}"; do
        [ -z "$n" ] && continue
        [ "$n" == "$name" ] && continue
        kept+=("$n")
    done
    RUNNING_INSTANCES=("${kept[@]:-}")
}

test_version() {
    local ver="$1"
    local name="it-$(echo "$ver" | tr '.' '-')"

    log "[$ver] INSTALL: begin"
    if ! "${DBPOD[@]}" engine install "mysql@$ver"; then
        log "[$ver] INSTALL: FAILED"
        FAILED_STEPS+=("install mysql@$ver")
        return 1
    fi
    log "[$ver] INSTALL: done"

    log "[$ver] RUN: begin ($name)"
    if ! "${DBPOD[@]}" run -d --name "$name" --engine "mysql@$ver" --data "$DATA_ROOT/$name"; then
        log "[$ver] RUN: FAILED to start"
        FAILED_STEPS+=("run $name")
        return 1
    fi
    RUNNING_INSTANCES+=("$name")
    log "[$ver] RUN: started"

    log "[$ver] VERIFY: begin (SELECT 1 over socket)"
    verify_out=$("${DBPOD[@]}" exec "$name" -e "SELECT 1" 2>&1)
    if [ $? -eq 0 ]; then
        log "[$ver] VERIFY: ok (SELECT 1 -> $(echo "$verify_out" | tail -1 | tr -d '[:space:]'))"
    else
        log "[$ver] VERIFY: FAILED: $(echo "$verify_out" | tail -1)"
        FAILED_STEPS+=("exec $name")
    fi

    log "[$ver] KILL: begin"
    "${DBPOD[@]}" kill "$name"
    log "[$ver] KILL: done"

    log "[$ver] RM: begin"
    "${DBPOD[@]}" rm -f "$name"
    log "[$ver] RM: done"

    remove_from_running "$name"
    return 0
}

rm_engine() {
    local ver="$1"
    log "CLEANUP: engine rm mysql@$ver — begin"
    if "${DBPOD[@]}" engine rm "mysql@$ver"; then
        log "CLEANUP: engine rm mysql@$ver — done"
    else
        log "CLEANUP: engine rm mysql@$ver — FAILED"
        FAILED_STEPS+=("engine rm mysql@$ver")
    fi
}

OVERALL_START=$(date +%s)
for series in $(echo "$PLAN_SERIES" | sort -V); do

    log "==== SERIES $series: begin ===="
    local_min=$(min_of "$series")
    local_max=$(max_of "$series")

    test_version "$local_min" || true
    if [ "$local_min" != "$local_max" ]; then
        test_version "$local_max" || true
    else
        log "[$series] single installable version — max test skipped"
    fi

    rm_engine "$local_min"
    [ "$local_min" != "$local_max" ] && rm_engine "$local_max"
    log "==== SERIES $series: done ===="
done

ELAPSED=$(( $(date +%s) - OVERALL_START ))
log "==== MATRIX FINISHED in ${ELAPSED}s ===="
if [ "${#FAILED_STEPS[@]}" -gt 0 ]; then
    log "FAILED STEPS (${#FAILED_STEPS[@]}):"
    for f in "${FAILED_STEPS[@]}"; do
        log "  - $f"
    done
    exit 1
fi
log "all steps passed"
