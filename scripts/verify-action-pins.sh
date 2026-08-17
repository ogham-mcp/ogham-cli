#!/usr/bin/env bash
# Verify every SHA-pinned GitHub Action matches the version in its comment.
#
# Why this exists (TBU-234, and the lesson from TBU-187): on a SHA-pinned
# action the trailing comment is the ONLY human-readable record of what
# actually executes. In the Python repo, 2 of 11 comments were FALSE --
# setup-uv labelled "# v5.4.2" was running v8.1.0, and Dependabot itself
# wrote one of the wrong labels while correctly updating the SHA.
#
# A wrong comment is worse than no comment: it is a confident, checked-in
# statement that nobody re-verifies.
#
# Exit 0 = every comment agrees with its SHA. Exit 1 = at least one lies.
# Exit 2 = could not check (missing gh, no auth, unparseable pin).

set -euo pipefail

command -v gh >/dev/null || { echo "verify-action-pins: gh not found" >&2; exit 2; }
gh auth status >/dev/null 2>&1 || { echo "verify-action-pins: gh not authenticated" >&2; exit 2; }

fail=0
checked=0

# Resolve a tag to the commit it points at, dereferencing annotated tags.
resolve_tag() {
    local repo=$1 tag=$2 type sha obj
    obj=$(gh api "repos/$repo/git/ref/tags/$tag" --jq '.object | "\(.type) \(.sha)"' 2>/dev/null) || return 1
    type=${obj%% *}
    sha=${obj##* }
    if [ "$type" = "tag" ]; then
        sha=$(gh api "repos/$repo/git/tags/$sha" --jq '.object.sha' 2>/dev/null) || return 1
    fi
    printf '%s' "$sha"
}

while IFS= read -r line; do
    # uses: owner/repo@<40-hex> # <version>
    if [[ $line =~ uses:[[:space:]]*([^@[:space:]]+)@([0-9a-f]{40})[[:space:]]*#[[:space:]]*(.+)$ ]]; then
        repo="${BASH_REMATCH[1]}"
        pinned="${BASH_REMATCH[2]}"
        claimed="$(echo "${BASH_REMATCH[3]}" | tr -d '[:space:]')"
        checked=$((checked + 1))

        if ! actual=$(resolve_tag "$repo" "$claimed"); then
            echo "UNRESOLVED  $repo  comment claims '$claimed', which is not a tag" >&2
            fail=1
            continue
        fi

        if [ "$actual" = "$pinned" ]; then
            printf 'ok          %s %s\n' "$repo" "$claimed"
        else
            echo "MISMATCH    $repo  comment says $claimed ($actual) but pin is $pinned" >&2
            fail=1
        fi
    # A bare tag ref is not a pin at all.
    elif [[ $line =~ uses:[[:space:]]*([^@[:space:]]+)@([^[:space:]]+) ]]; then
        # Capture BOTH groups before any further regex test -- an inner
        # [[ =~ ]] clobbers BASH_REMATCH, and under `set -u` reading it
        # afterwards aborts the script instead of reporting the finding.
        unpinned_repo="${BASH_REMATCH[1]}"
        ref="${BASH_REMATCH[2]}"
        if [[ ! $ref =~ ^[0-9a-f]{40}$ ]]; then
            echo "UNPINNED    $unpinned_repo@$ref  is a mutable tag, not a SHA" >&2
            fail=1
        fi
    fi
done < <(grep -h "uses:" .github/workflows/*.yml)

if [ "$checked" -eq 0 ] && [ "$fail" -eq 0 ]; then
    echo "verify-action-pins: no pinned actions found -- is the glob right?" >&2
    exit 2
fi

if [ "$fail" -ne 0 ]; then
    echo "verify-action-pins: FAILED -- a pin comment does not match its SHA" >&2
    exit 1
fi

echo "verify-action-pins: $checked pin(s) verified"
