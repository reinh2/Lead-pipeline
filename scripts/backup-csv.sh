#!/bin/sh
set -eu

source_path=${1:?usage: backup-csv.sh PATH_TO_LEADS_CSV BACKUP_DIRECTORY}
backup_dir=${2:?usage: backup-csv.sh PATH_TO_LEADS_CSV BACKUP_DIRECTORY}

if [ ! -f "$source_path" ]; then
  echo "CSV fallback does not exist: $source_path" >&2
  exit 1
fi

mkdir -p "$backup_dir"
timestamp=$(date -u +%Y%m%dT%H%M%SZ)
target="$backup_dir/leads-$timestamp.csv"
cp "$source_path" "$target"
shasum -a 256 "$target" > "$target.sha256"
echo "Backup created: $target"
