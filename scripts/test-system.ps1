param(
    # Explicit -PostgresDsn wins; otherwise an already-set COMPAT_POSTGRES_DSN
    # is respected (docs/TESTING.md tells users to set it); the localhost
    # default is the last resort.
    [string]$PostgresDsn = "",
    [string]$E2ERun = ""
)

$ErrorActionPreference = 'Stop'
if ($PostgresDsn -ne '') {
    $env:COMPAT_POSTGRES_DSN = $PostgresDsn
} elseif (-not $env:COMPAT_POSTGRES_DSN) {
    $env:COMPAT_POSTGRES_DSN = "postgres://$env:USERNAME@127.0.0.1:5432/postgres?sslmode=disable"
}

# The e2e suite has exactly one intentional, documented failure:
# TestSystemClaimsExactCoverageForRequiredFeatureFamilies asserts exact
# coverage for feature families (foreign_keys, check_constraints, indexes,
# triggers, views, stored_routines, full_text) that are `unknown` by design
# until an exact adapter exists for them (see compat/features.go and
# docs/reports/AUDIT7-C.md, finding H2). Treat it as a documented contract,
# not a bug: do not skip it, do not delete it.
$IntentionalFailTest = 'TestSystemClaimsExactCoverageForRequiredFeatureFamilies'

go test ./... -timeout 60s
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

go vet ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

# Run the e2e suite with -json so top-level PASS/FAIL results can be parsed
# reliably, instead of scraping -v text output. -E2ERun is an optional
# override (e.g. for diagnosing this script itself) that narrows the suite
# with go test's -run filter; it is not needed for normal use.
$e2eArgs = @('test', '-tags=e2e', './e2e', '-json', '-count=1', '-timeout', '60s')
if ($E2ERun -ne '') { $e2eArgs += @('-run', $E2ERun) }
$jsonLines = & go @e2eArgs

# Collect PASS/FAIL per top-level test (Test names containing "/" are
# subtests of a top-level test and are not evaluated on their own).
$results = @{}
foreach ($line in $jsonLines) {
    if (-not $line) { continue }
    try { $testEvent = $line | ConvertFrom-Json } catch { continue }
    if (-not $testEvent.Test) { continue }
    if ($testEvent.Test.Contains('/')) { continue }
    if ($testEvent.Action -eq 'pass' -or $testEvent.Action -eq 'fail') {
        $results[$testEvent.Test] = $testEvent.Action
    }
}

if ($results.Count -eq 0) {
    Write-Host "test-system: no test results parsed from the e2e run (build or setup failure before any test ran)." -ForegroundColor Red
    Write-Host "Raw go test output:" -ForegroundColor Red
    $jsonLines | ForEach-Object {
        if (-not $_) { return }
        try { $evt = $_ | ConvertFrom-Json; if ($evt.Output) { Write-Host -NoNewline $evt.Output } } catch { Write-Host $_ }
    }
    exit 1
}

$failedTests = $results.GetEnumerator() | Where-Object { $_.Value -eq 'fail' } | ForEach-Object { $_.Key }
$unexpectedFailures = $failedTests | Where-Object { $_ -ne $IntentionalFailTest }

if ($unexpectedFailures) {
    Write-Host "test-system: e2e suite has failures beyond the documented baseline:" -ForegroundColor Red
    $unexpectedFailures | ForEach-Object { Write-Host "  $_" -ForegroundColor Red }
    exit 1
}

if ($failedTests -notcontains $IntentionalFailTest) {
    Write-Host "test-system: expected intentional failure '$IntentionalFailTest' did not fail (it passed or did not run)." -ForegroundColor Red
    Write-Host "Either exact coverage improved for a previously-unknown feature family (update docs/COMPATIBILITY.md and this script's baseline), or the -run filter excluded it, or something else changed. Investigate before treating this as green." -ForegroundColor Red
    exit 1
}

Write-Host "test-system: OK - the only e2e failure is the documented baseline '$IntentionalFailTest' (docs/reports/AUDIT7-C.md, finding H2)." -ForegroundColor Green
exit 0
