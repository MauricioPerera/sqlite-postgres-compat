param(
    [string]$PostgresDsn = "postgres://$env:USERNAME@127.0.0.1:5432/postgres?sslmode=disable"
)

$ErrorActionPreference = 'Stop'
$env:COMPAT_POSTGRES_DSN = $PostgresDsn

go test ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

go vet ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

go test -tags=e2e ./e2e -v -count=1
exit $LASTEXITCODE
