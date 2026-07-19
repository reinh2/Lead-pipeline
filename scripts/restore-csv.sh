#!/bin/sh
set -eu

backup_path=${1:?usage: restore-csv.sh BACKUP_CSV PATH_TO_LEADS_CSV}
destination_path=${2:?usage: restore-csv.sh BACKUP_CSV PATH_TO_LEADS_CSV}

if [ ! -f "$backup_path" ]; then
  echo "Backup does not exist: $backup_path" >&2
  exit 1
fi

if [ -f "$backup_path.sha256" ]; then
  (cd "$(dirname "$backup_path")" && shasum -a 256 -c "$(basename "$backup_path").sha256")
fi

mkdir -p "$(dirname "$destination_path")"
temporary_path="$destination_path.restore.$$"
cp "$backup_path" "$temporary_path"
mv "$temporary_path" "$destination_path"
echo "Restored CSV fallback to: $destination_path"
