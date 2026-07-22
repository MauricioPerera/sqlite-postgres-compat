# Quality gate: formatting, vet and unit tests. Run from the repo root.
$ErrorActionPreference = "Stop"

$unformatted = gofmt -l .
if ($unformatted) {
    Write-Host "gofmt: the following files are not formatted:" -ForegroundColor Red
    $unformatted | ForEach-Object { Write-Host "  $_" }
    exit 1
}
Write-Host "gofmt: OK"

go vet ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Write-Host "go vet: OK"

go test ./... -count=1
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Write-Host "go test: OK"
