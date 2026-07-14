# Task 1 Report: Persist Unit Substage State

## RED

Command:

```bash
rtk go test ./internal/source -run 'TestStoreUnitStageLifecycle|TestStoreUnitsStatusTimestamps' -count=1
```

Result: FAIL (expected). The new lifecycle test did not compile because `Source.UnitsStage`, `Source.UnitsBuiltAt`, `(*Store).StartUnitsProcessing`, and `(*Store).MarkUnitsSemanticsStarted` did not exist.

## GREEN

Command:

```bash
rtk go test ./internal/source -run 'TestStoreUnitStageLifecycle|TestStoreUnitsStatusTimestamps' -count=1
```

Result: PASS (1 package, 1 test).

Additional verification:

```bash
rtk go test ./internal/source -count=1
```

Result: PASS (1 package, 93 tests).

## Files Changed

- `internal/foundation/db/migrations/022_units_stage.sql`
- `internal/source/store.go`
- `internal/source/store_test.go`

## Commit

`f884bac4a82ec979c3d0cbb7eebf13263a5a85db` (`feat: persist unit semantic stage`)

## Self-Review

- Added the migration with the required `units_stage` default and nullable `units_built_at` timestamp.
- Included `units_stage` in `GetByID`, `List`, and `GetShadowByTarget` selects/scans; included `units_built_at` where unit timestamps are already read.
- Verified `StartUnitsProcessing` resets both unit timestamps and moves to the building substage; `MarkUnitsSemanticsStarted` records the semantic transition; terminal unit statuses retain their timestamp behavior; non-terminal unit statuses clear `units_completed_at`.
- Added lifecycle and list-read coverage using the real test database. No remaining issues found in the scoped review.

## Review Finding Fix: Clear Completed Timestamp on Non-Terminal Status

### RED

The new regression test was run against a temporary simulation of the missing non-terminal `units_completed_at = NULL` assignment:

```text
$ rtk go test ./internal/source -run 'TestStoreUpdateUnitsStatusClearsCompletedAtForNonTerminalStatus' -count=1
Go test: 0 passed, 1 failed in 1 packages
source (0 passed, 1 failed)
  [FAIL] TestStoreUpdateUnitsStatusClearsCompletedAtForNonTerminalStatus
     store_test.go:144: units_completed_at should be NULL after a non-terminal status
exit_code=1
```

### GREEN

After restoring the current implementation:

```text
$ rtk go test ./internal/source -run 'TestStoreUpdateUnitsStatus|TestStoreUnitStageLifecycle' -count=1
Go test: 3 passed in 1 packages
exit_code=0

$ rtk go test ./internal/source -count=1
Go test: 94 passed in 1 packages
exit_code=0
```

### Fix Commit

`805ceab4718740caec0c78c190249d633ad0e1c6` (`test: cover units completion timestamp reset`)
