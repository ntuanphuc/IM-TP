# Install ChatTP

The stack includes a Caddy reverse proxy, Go server, Go worker, PostgreSQL, Redis, MinIO, and ClamAV. The proxy serves the two apps on separate hostnames. By default, the stack listens on loopback and uses Caddy's internal CA.

## Docker Desktop on Windows

1. Install [Docker Desktop for Windows](https://docs.docker.com/desktop/setup/install/windows-install/), enable WSL 2, and select Linux containers. Start Docker Desktop and open PowerShell in the directory containing `compose.yaml`.
2. Create `.env` using the following commands. They stop if `.env` already exists. Open the file to retrieve the local administrator password when signing in.

```powershell
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
```

3. If Active Directory is available, set the variables in [Active Directory](#active-directory). With AD left blank, `local-admin` can sign in to both apps and create a private conversation for that account. AD provides directory synchronization and multi-user chat.
4. Build and start the stack. On the first run, Docker downloads base images, Go modules, and npm packages. Database migrations and bucket creation run automatically.

```powershell
docker compose up -d --build
docker compose ps -a
```

The `migrate` service applies migrations, creates the bucket, and exits with code 0. Compose builds MinIO from the source version pinned in `deploy/minio.Dockerfile`. If a previous run returned `401 UNAUTHORIZED` from Quay, update the source and rerun `docker compose up -d --build` in the same directory. This reuses `.env` and the existing stack's named volumes. On Dockhand, sync the updated source and enable image building at deployment. [MinIO release](https://github.com/minio/minio/releases/tag/RELEASE.2025-10-15T17-29-55Z)

ClamAV may take a few minutes to download its first signature database. Files remain pending while the scanner starts.

5. Export the CA certificate from the gateway of the running stack and add it to the current Windows user's trust store.

```powershell
docker compose cp gateway:/data/caddy/pki/authorities/local/root.crt ./chattp-local-ca.crt
Import-Certificate -FilePath (Resolve-Path .\chattp-local-ca.crt) -CertStoreLocation Cert:\CurrentUser\Root
```

In **Command Prompt (CMD)**, run the following command from the directory containing `chattp-local-ca.crt`.

```cmd
certutil -user -addstore Root "chattp-local-ca.crt"
```

Restart the browser. If it uses its own CA store, import the same certificate there. If the hostnames below do not resolve, add `127.0.0.1 chat.localhost admin.localhost` to the Windows hosts file with administrator privileges.

| App | Address |
|---|---|
| Web chat | [https://chat.localhost:8443](https://chat.localhost:8443) |
| Admin | [https://admin.localhost:8443](https://admin.localhost:8443) |

Use `docker compose logs -f gateway server worker` to view logs. Run `docker compose down` to stop the stack and preserve named volumes. To update the code, rerun `docker compose up -d --build`.

## Ubuntu on Windows with WSL 2

In Docker Desktop, go to **Settings → Resources → WSL Integration** and enable Ubuntu integration. Run the following commands in Ubuntu.

```bash
cd /mnt/c/Users/v12683/Documents/ChatGPT/ChatTP
bash deploy/init-env.sh
docker compose up -d --build
docker compose ps -a
docker compose cp gateway:/data/caddy/pki/authorities/local/root.crt ./chattp-local-ca.crt
```

The script stops if `.env` already exists. If a previous attempt left a file with missing values, rename it to keep a copy and rerun the script. If Ubuntu needs OpenSSL, run `sudo apt update && sudo apt install openssl`.

If the browser runs on Windows, install the CA in the current Windows user's certificate store. Open `chattp-local-ca.crt` in File Explorer and select **Current User → Trusted Root Certification Authorities**, or call the Windows tool from Ubuntu as shown below.

```bash
certutil.exe -user -addstore Root "$(wslpath -w "$PWD/chattp-local-ca.crt")"
```

Open [web admin](https://admin.localhost:8443) or [web chat](https://chat.localhost:8443) and sign in as `local-admin` with the `LOCAL_ADMIN_PASSWORD` from `.env`.

## Dockhand

ChatTP uses the same `compose.yaml` on the Docker Engine managed by Dockhand. The manual describes Git stacks and Docker environment setup. [Dockhand User Manual](https://dockhand.pro/manual/)

### Install Dockhand on Docker Desktop or Linux

If Dockhand is missing, run the following from the project directory on a host with Docker Engine.

```powershell
docker compose -p dockhand -f deploy/dockhand.compose.yaml up -d
```

Open [the local Dockhand dashboard](http://localhost:3000), create a Dockhand administrator account, and select the local Docker environment. The container needs the Docker socket to manage stacks, so the interface binds to loopback. For a remote host, access the dashboard through an administrative tunnel or use the school's configured Dockhand instance.

### Deploy ChatTP from Git

1. Push the source to a Git repository accessible to Dockhand, including `compose.yaml`, `backend/`, `apps/`, `packages/`, and `deploy/`. Keep `.env` and private certificates outside Git.
2. In **Stacks**, select **From Git**. Choose the Docker environment, repository, and branch. Set the Compose path to `compose.yaml` and enable image builds if your Dockhand version offers that option. The repository root is the build context.
3. Set the variables from `.env.example` in the stack environment. Give the stack a distinct name such as `chattp`. If ChatTP is already running from the CLI on the same host, stop that stack before reusing its ports. The stacks use separate volumes based on project name.
4. Deploy the stack. On the Docker host, export the CA from the stack's gateway with `docker cp <gateway-container>:/data/caddy/pki/authorities/local/root.crt ./chattp-local-ca.crt`. For internal TLS, import the CA on the client machine. For a dedicated host, configure DNS and the bind address as described below.

For local-only source, run `docker compose up -d --build` as shown in the Windows instructions and use Dockhand to view containers and logs in the same Docker environment. A Git stack can redeploy directly from its source.

### DNS and TLS on the school network

Set `CHAT_HOST` and `ADMIN_HOST` to two internal DNS names that resolve to the Docker host. Set `BIND_IP` to its internal address or `0.0.0.0` and open `HTTPS_PORT` in the firewall. Compose derives `WEB_ORIGINS` from these values. Use internal TLS and distribute the CA through IT, or update `deploy/Caddyfile` to use the school's certificates and rebuild the gateway image. For the default HTTPS port, set `HTTPS_PORT=443`. The Go service normalizes the allowlist generated by Compose.

## Two demo chat accounts without AD

Keep `AD_URLS=` and `BIND_IP=127.0.0.1` in `.env`. Set `DEMO_USER_1_PASSWORD` and `DEMO_USER_2_PASSWORD` to distinct passwords of at least 16 bytes each. Run `docker compose up -d --build`. The `migrate` service creates the two accounts when the stack starts with the new configuration.

- `chat-demo-01` uses `DEMO_USER_1_PASSWORD`.
- `chat-demo-02` uses `DEMO_USER_2_PASSWORD`.

Sign in to [web chat](https://chat.localhost:8443) with each account in a separate browser session. Choose **New message → Direct message**, find the other account, and select **Message**. Both accounts are regular chat users with teacher permissions for testing DMs. Sign in to [web admin](https://admin.localhost:8443) with `LOCAL_ADMIN_USER` and `LOCAL_ADMIN_PASSWORD`.

To disable the demo accounts, clear both password values and run `docker compose up -d`. Startup initialization disables both accounts and revokes their sessions.

## Active Directory

- `AD_URLS` accepts comma-separated endpoints, for example `ldaps://dc1.school.example:636,ldaps://dc2.school.example:636`. The Docker host must be able to resolve the domain controllers and reach their LDAPS ports.
- `AD_BASE_DN` defines the synchronization scope. `AD_BIND_DN` and `AD_BIND_PASSWORD` identify the directory read account. Use consistent scopes and read permissions across domain controllers. The worker reconciles missing accounts after a successful paginated read.
- `AD_CA_PEM_BASE64` holds the base64-encoded PEM chain for the LDAP CA. In PowerShell, use `[Convert]::ToBase64String([IO.File]::ReadAllBytes((Resolve-Path .\ad-ca.pem)))`. Leave it blank if the CA is already in the container's trust store.
- `AD_TEACHER_GROUP_DN`, `AD_STAFF_GROUP_DN`, and `AD_ADMIN_GROUP_DN` take full DNs and match direct memberships. The current implementation classifies AD users outside these groups as students. Use an admin group of at least two people if you enable two-person approval for sensitive segments.
- Choose a `LOCAL_ADMIN_USER` distinct from AD usernames. Break-glass access uses a secret in the container configuration. Leave `LOCAL_ADMIN_PASSWORD` blank to disable break-glass sign-in. After changing LDAP configuration, recreate the containers with Compose or redeploy from Dockhand.

## OpenSearch

To enable Vietnamese message search with or without diacritics on the pilot host, set `OPENSEARCH_URL=http://opensearch:9200` in `.env` and run `docker compose --profile search up -d --build`. In Dockhand, enable the Compose `search` profile or set `COMPOSE_PROFILES=search` in the deployment environment. The image installs the ICU plugin during the build. If OpenSearch reports a low `vm.max_map_count`, apply the Docker host or WSL 2 settings in the official guide. [OpenSearch Docker guide](https://docs.opensearch.org/latest/install-and-configure/install-opensearch/docker/)

The OpenSearch container in the profile is for pilot use and is accessible only within the stack. For a school-managed OpenSearch deployment, set `OPENSEARCH_URL` to its TLS endpoint and configure `OPENSEARCH_USER` and `OPENSEARCH_PASSWORD` with an account scoped to the ChatTP index. The backend rechecks access in PostgreSQL. Searches fall back to PostgreSQL when the index is unavailable or returns an error.

## Current scope

The repository includes the backend, web chat, and web admin. Implemented flows include AD authentication with refresh token rotation, directory synchronization, private channels and direct and group messages with basic safeguards, timelines, threads, reactions, ClamAV scanning, permission-filtered search, manual segment management, two-person approval, single-segment web broadcasts, and audit events for key actions. WebSocket uses a global event cursor, Redis signals new events, and PostgreSQL provides replay. Each broadcast uses an announcement channel. Optional OpenSearch provides ICU support for message search. The API serves clean files after an authorization check. Admin access is limited to the configured AD group and break-glass accounts.

Administrators can configure allow and deny rules for manual segments. Deny takes precedence and student safeguards continue to apply. The retention screen sets the number of days by conversation type or across the system. When retention is enabled, the worker clears expired content, updates the index, and queues attachments for cleanup. The database preserves expired message IDs and sequence numbers to maintain message ordering.

Work remaining from specification v1.2 includes conditional rules and manager-based authorization, rule-based segment synchronization, membership review and expiry, file, user, and channel indexing, JetStream, mobile push, audience expressions, per-channel retention, presence and typing, full observability, and high availability. This implementation uses `pgx` directly and a handwritten TypeScript client. The Compose stack runs one server and one worker. Complete security, load, and recovery tests before deploying with student data. For the pilot verification recorded here, Compose configuration parsed successfully and tests were skipped as requested. The build and end-to-end deployment were unverified because dependencies were missing from the local cache and access to the Go module proxy was blocked. [API routes](../api/openapi.yaml) · [WebSocket protocol](../api/realtime/README.md)
