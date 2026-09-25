#!/usr/bin/env bash
# Downloads one Laya Core ML checkpoint into .models/<name> (gitignored), the
# directory LayaKit's checkpoint tests look in; they skip when it is absent.
#   scripts/fetch-model.sh [--tokenizer] [repo-id]
#     default repo aac6fef/laya-typed-decisions-coreml; --tokenizer fetches only
#     tokenizer/ and the configs (what the tokenizer and prompt tests need)
# Resumes a partial download and checks every file against the SHA-256 in the
# checkpoint's own coreml_config.json.
set -euo pipefail
cd "$(dirname "$0")/.."

only=""
if [ "${1:-}" = "--tokenizer" ]; then only="tokenizer/"; shift; fi
repo="${1:-aac6fef/laya-typed-decisions-coreml}"
dest=".models/${repo##*/}"
hub="https://huggingface.co"
mkdir -p "$dest"

size() { stat -f %z "$1" 2>/dev/null || stat -c %s "$1" 2>/dev/null || echo 0; }

echo "  ↓ $repo → $dest"
listing=$(curl -fsSL "$hub/api/models/$repo/tree/main?recursive=1" | python3 -c '
import json, sys
only = sys.argv[1]
for f in json.load(sys.stdin):
    p = f["path"]
    if f["type"] != "file" or p.startswith("."):
        continue
    if only and not (p.startswith(only) or p in ("coreml_config.json", "rl_agent_config.json")):
        continue
    print(f["size"], p)
' "$only")

while read -r bytes path; do
  mkdir -p "$dest/$(dirname "$path")"
  tries=0
  # -C - resumes where a dropped connection left the file.
  while [ "$(size "$dest/$path")" != "$bytes" ]; do
    tries=$((tries + 1))
    if [ "$tries" -gt 5 ]; then echo "  ✗ $path: gave up after 5 tries" >&2; exit 1; fi
    curl -fsSL -C - -o "$dest/$path" "$hub/$repo/resolve/main/$path" || sleep 2
  done
  echo "  ✓ $path"
done <<<"$listing"

python3 - "$dest" "$only" <<'PY'
import hashlib, json, pathlib, sys
root = pathlib.Path(sys.argv[1])
files = json.loads((root / "coreml_config.json").read_text())["files"]
bad = []
for name, want in files.items():
    if sys.argv[2] and not (root / name).exists():
        continue  # --tokenizer
    h = hashlib.sha256()
    with open(root / name, "rb") as f:
        for block in iter(lambda: f.read(8 << 20), b""):
            h.update(block)
    if h.hexdigest() != want["sha256"]:
        bad.append(name)
if bad:
    sys.exit("  ✗ checksum mismatch: " + ", ".join(bad) + " (delete them and run again)")
print("  ✓ files verified")
PY
