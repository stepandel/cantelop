#!/usr/bin/env bash
set -Eeuo pipefail

repository_directory="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"

go test "${repository_directory}/fire-fuse/runtime/microvm" \
  -run 'Test(OpenSQLiteStoreInitializesSchema|OpenSQLiteStoreReopensCurrentSchema|SQLiteStorePersistsLifecycleMutations|SQLiteStorePersistsRuntimeCheckpointAcrossReopen|SQLiteConfigSecretsAreEncryptedAtRest)$' \
  -count=1
go test "${repository_directory}/fire-fuse/runtime/persistence/sqliteschema" \
  -run 'TestInitialize(Create|Reject|Migrate)' -count=1

printf 'Qualified clean schema-v28 creation, reopen, v24/v25/v26/v27 migration, and pre-cutoff schema rejection paths\n'
