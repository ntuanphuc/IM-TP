if (Test-Path -LiteralPath .env) { throw '.env already exists' }
$rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
try {
  $config = Get-Content -LiteralPath .env.example -Raw
  foreach ($key in 'POSTGRES_PASSWORD','REDIS_PASSWORD','JWT_SECRET','MINIO_SECRET_KEY','LOCAL_ADMIN_PASSWORD') {
    $bytes = New-Object byte[] 32
    $rng.GetBytes($bytes)
    $value = [BitConverter]::ToString($bytes).Replace('-','').ToLowerInvariant()
    $config = [regex]::Replace($config, "(?m)^${key}=\r?$", "$key=$value")
  }
  [IO.File]::WriteAllText((Join-Path (Get-Location).Path '.env'), $config, (New-Object System.Text.UTF8Encoding($false)))
} finally {
  $rng.Dispose()
}