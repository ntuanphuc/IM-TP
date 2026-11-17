# Cài đặt ChatTP

Stack gồm reverse proxy Caddy, Go server, Go worker, PostgreSQL, Redis, MinIO và ClamAV. Reverse proxy phục vụ cả hai ứng dụng trên hai hostname. Bản cài mặc định chỉ lắng nghe loopback và dùng CA nội bộ do Caddy tạo.

## Docker Desktop trên Windows

1. Cài [Docker Desktop cho Windows](https://docs.docker.com/desktop/setup/install/windows-install/), bật WSL 2 và chọn Linux containers. Khởi động Docker Desktop, rồi mở PowerShell tại thư mục có `compose.yaml`.
2. Tạo `.env` lần đầu bằng đoạn lệnh dưới đây. Đoạn lệnh sẽ dừng nếu đã có `.env`. Mở file để đọc mật khẩu quản trị cục bộ khi đăng nhập.

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

3. Nếu có AD, điền các biến theo phần [Active Directory](#active-directory). Nếu để trống AD, có thể dùng `local-admin` để vào cả hai app và tạo hội thoại riêng cho tài khoản đó. Đồng bộ và chat nhiều người cần AD.
4. Build và khởi động. Docker tải base images, Go modules và gói npm trong lần đầu. Migration và tạo bucket chạy tự động.

```powershell
docker compose up -d --build
docker compose ps -a
```

Service `migrate` chạy migration, tạo bucket rồi thoát mã 0. Compose build MinIO từ bản mã nguồn đã ghim trong `deploy/minio.Dockerfile`. Nếu lần chạy trước gặp lỗi `401 UNAUTHORIZED` từ Quay, cập nhật source rồi chạy lại `docker compose up -d --build` trong cùng thư mục. Lệnh này dùng lại `.env` và named volumes của stack hiện tại. Trên Dockhand, đồng bộ source mới và bật build image khi deploy. [Bản phát hành MinIO](https://github.com/minio/minio/releases/tag/RELEASE.2025-10-15T17-29-55Z)

ClamAV có thể cần vài phút để tải chữ ký lần đầu. File còn ở trạng thái chờ trong thời gian scanner khởi động.

5. Trích xuất CA từ đúng gateway của stack vừa khởi động và thêm vào kho tin cậy của tài khoản Windows hiện tại.

```powershell
docker compose cp gateway:/data/caddy/pki/authorities/local/root.crt ./chattp-local-ca.crt
Import-Certificate -FilePath (Resolve-Path .\chattp-local-ca.crt) -CertStoreLocation Cert:\CurrentUser\Root
```

Trong **Command Prompt (CMD)**, chạy lệnh dưới đây từ thư mục có `chattp-local-ca.crt`:

```cmd
certutil -user -addstore Root "chattp-local-ca.crt"
```

Mở lại trình duyệt. Nếu trình duyệt có kho CA riêng, nhập cùng chứng chỉ vào kho đó. Nếu hai tên dưới đây không phân giải trên máy, thêm `127.0.0.1 chat.localhost admin.localhost` vào file hosts của Windows với quyền quản trị.

| Ứng dụng | Địa chỉ |
|---|---|
| Web chat | [https://chat.localhost:8443](https://chat.localhost:8443) |
| Quản trị | [https://admin.localhost:8443](https://admin.localhost:8443) |

`docker compose logs -f gateway server worker` xem log. `docker compose down` dừng stack và giữ named volumes. Khi cần cập nhật code, chạy lại `docker compose up -d --build`.

## Ubuntu trên Windows qua WSL 2

Mở Docker Desktop, vào **Settings → Resources → WSL Integration** và bật tích hợp cho Ubuntu. Trong Ubuntu, chạy:

```bash
cd /mnt/c/Users/v12683/Documents/ChatGPT/ChatTP
bash deploy/init-env.sh
docker compose up -d --build
docker compose ps -a
docker compose cp gateway:/data/caddy/pki/authorities/local/root.crt ./chattp-local-ca.crt
```

Script dừng nếu `.env` đã tồn tại. Nếu lần tạo trước để lại file thiếu giá trị, hãy đổi tên file đó để giữ bản sao rồi chạy lại script. Nếu Ubuntu thiếu OpenSSL, chạy `sudo apt update && sudo apt install openssl`.

Nếu dùng trình duyệt trên Windows, cài CA vào kho chứng chỉ của tài khoản Windows hiện tại. Có thể mở `chattp-local-ca.crt` bằng File Explorer và chọn **Current User → Trusted Root Certification Authorities**, hoặc gọi công cụ Windows ngay từ Ubuntu:

```bash
certutil.exe -user -addstore Root "$(wslpath -w "$PWD/chattp-local-ca.crt")"
```

Sau đó mở [web quản trị](https://admin.localhost:8443) hoặc [web chat](https://chat.localhost:8443) và đăng nhập bằng `local-admin` cùng mật khẩu `LOCAL_ADMIN_PASSWORD` trong `.env`.

## Dockhand

ChatTP dùng cùng `compose.yaml` trên Docker Engine mà Dockhand quản lý. [Dockhand User Manual](https://dockhand.pro/manual/) mô tả Git stacks và thiết lập môi trường Docker.

### Cài Dockhand trên Docker Desktop hoặc Linux

Nếu chưa có Dockhand, chạy từ thư mục dự án ở máy có Docker Engine:

```powershell
docker compose -p dockhand -f deploy/dockhand.compose.yaml up -d
```

Mở [http://localhost:3000](http://localhost:3000), tạo tài khoản quản trị Dockhand và chọn Docker environment cục bộ. Container Dockhand cần Docker socket để quản lý stack, vì vậy giao diện được bind loopback. Với host từ xa, truy cập dashboard qua tunnel quản trị hoặc dùng Dockhand đã được trường cấu hình.

### Triển khai ChatTP từ Git

1. Đưa source lên Git repository mà Dockhand truy cập được, gồm `compose.yaml`, `backend/`, `apps/`, `packages/` và `deploy/`. Giữ `.env` và chứng chỉ riêng ngoài Git.
2. Trong **Stacks**, chọn triển khai **From Git**. Chọn Docker environment, repo, nhánh và Compose path `compose.yaml`. Bật build image khi triển khai nếu bản Dockhand hiển thị tùy chọn này. Build context là thư mục gốc repository.
3. Điền biến từ `.env.example` trong cấu hình môi trường của stack. Đặt tên stack riêng, ví dụ `chattp`. Nếu đã chạy ChatTP bằng CLI trên cùng host, dừng stack CLI trước khi dùng lại cùng cổng. Hai stack có volume riêng theo tên dự án.
4. Deploy. Lấy CA từ gateway của stack trên Docker host, dùng lệnh `docker cp <gateway-container>:/data/caddy/pki/authorities/local/root.crt ./chattp-local-ca.crt`. Nhập CA vào máy truy cập nếu dùng TLS nội bộ. Với host riêng, dùng DNS và cấu hình bind như mục bên dưới.

Nếu source chỉ có trên máy, có thể dùng động tác `docker compose up -d --build` ở phần Windows và dùng Dockhand để xem container/log trên cùng Docker environment. Git stack hỗ trợ triển khai lại trực tiếp từ source.

### DNS và TLS cho mạng trường

Đặt `CHAT_HOST` và `ADMIN_HOST` thành hai DNS name nội bộ, trỏ tới máy Docker. Đặt `BIND_IP` thành IP nội bộ của host hoặc `0.0.0.0`, và mở đúng `HTTPS_PORT` trên firewall. `WEB_ORIGINS` được Compose tạo theo các giá trị này. Giữ TLS nội bộ và phân phối CA qua IT, hoặc sửa `deploy/Caddyfile` để dùng chứng chỉ của trường rồi build lại image gateway. Với cổng 443 là cổng mặc định của HTTPS, giữ `HTTPS_PORT=443`. Compose sẽ chuẩn hóa allowlist tại Go service.

## Hai tài khoản chat thử không cần AD

Giữ `AD_URLS=` và `BIND_IP=127.0.0.1` trong `.env`. Thêm hoặc điền `DEMO_USER_1_PASSWORD` và `DEMO_USER_2_PASSWORD` bằng hai mật khẩu khác nhau, mỗi mật khẩu tối thiểu 16 byte. Sau đó chạy `docker compose up -d --build`. Service `migrate` tạo hai tài khoản trong lần khởi động với cấu hình mới.

- `chat-demo-01` dùng `DEMO_USER_1_PASSWORD`.
- `chat-demo-02` dùng `DEMO_USER_2_PASSWORD`.

Đăng nhập hai tài khoản ở [web chat](https://chat.localhost:8443) bằng hai phiên trình duyệt riêng. Chọn **Tin mới → Nhắn riêng**, tìm tài khoản còn lại rồi bấm **Nhắn tin**. Cả hai là tài khoản chat thường có quyền giáo viên để thử DM. Vào [web quản trị](https://admin.localhost:8443) bằng `LOCAL_ADMIN_USER` và `LOCAL_ADMIN_PASSWORD`.

Để tắt tài khoản demo, xóa giá trị của hai biến mật khẩu và chạy `docker compose up -d`. Bootstrap vô hiệu hóa hai tài khoản demo và thu hồi phiên của chúng.

## Active Directory

- `AD_URLS` nhận danh sách phân tách bằng dấu phẩy, ví dụ `ldaps://dc1.school.example:636,ldaps://dc2.school.example:636`. Docker host cần truy cập được DNS và cổng LDAPS của DC.
- `AD_BASE_DN` xác định phạm vi đồng bộ. `AD_BIND_DN` và `AD_BIND_PASSWORD` là tài khoản đọc directory. Dùng phạm vi và quyền đọc nhất quán giữa các DC. Worker đối soát mất tài khoản sau lượt đọc phân trang thành công.
- `AD_CA_PEM_BASE64` chứa CA chain LDAP dạng PEM được mã hóa base64. Có thể tạo bằng PowerShell với `[Convert]::ToBase64String([IO.File]::ReadAllBytes((Resolve-Path .\ad-ca.pem)))`. Nếu CA đã nằm trong trust store của container, có thể để trống.
- `AD_TEACHER_GROUP_DN`, `AD_STAFF_GROUP_DN` và `AD_ADMIN_GROUP_DN` dùng DN đầy đủ và match membership trực tiếp. Tài khoản AD ngoài các nhóm này được xếp là học sinh trong bản hiện tại. Dùng group admin có ít nhất hai người nếu bật phê duyệt hai người cho segment nhạy cảm.
- Chọn `LOCAL_ADMIN_USER` không trùng tài khoản AD. Break-glass dùng secret trong cấu hình container. Để trống `LOCAL_ADMIN_PASSWORD` để vô hiệu đăng nhập break-glass. Đổi cấu hình LDAP xong, tạo lại container bằng Compose hoặc redeploy từ Dockhand.

## Tìm kiếm OpenSearch

Để bật tìm kiếm tin nhắn tiếng Việt có dấu và không dấu trên máy pilot, đặt `OPENSEARCH_URL=http://opensearch:9200` trong `.env`, rồi chạy `docker compose --profile search up -d --build`. Khi triển khai từ Dockhand, bật profile `search` của Compose hoặc đặt `COMPOSE_PROFILES=search` trong môi trường triển khai. Image cài plugin ICU khi build. Nếu OpenSearch báo giới hạn `vm.max_map_count` thấp, áp dụng cấu hình của [OpenSearch Docker](https://docs.opensearch.org/latest/install-and-configure/install-opensearch/docker/) cho Docker host hoặc WSL 2.

Container OpenSearch trong profile chỉ kết nối nội bộ stack, dùng cho pilot. Khi dùng OpenSearch đã được trường vận hành, đặt `OPENSEARCH_URL` tới endpoint TLS và cấu hình `OPENSEARCH_USER`/`OPENSEARCH_PASSWORD` cho tài khoản chỉ có quyền với index ChatTP. Backend kiểm tra lại quyền với PostgreSQL. Khi index chưa sẵn sàng hoặc lỗi, truy vấn quay về tìm trực tiếp trong PostgreSQL.

## Phạm vi hiện tại

Backend, web chat và web quản trị đã có mã nguồn. Các luồng đã triển khai gồm xác thực AD và refresh có xoay vòng, đồng bộ người dùng, hội thoại riêng/DM/nhóm theo luật bảo vệ cơ bản, timeline/luồng trả lời/reaction, file quét ClamAV, tìm tin theo quyền, quản lý segment thủ công, phê duyệt hai người, broadcast web theo một segment và audit các thao tác chính. WebSocket dùng cursor sự kiện toàn cục, Redis báo có sự kiện và Postgres phục vụ replay. Broadcast dùng một announcement channel cho từng lần gửi. OpenSearch tùy chọn hỗ trợ ICU cho tin nhắn. File sạch được truyền qua API sau khi kiểm tra quyền. Admin chỉ cấp cho nhóm AD cấu hình hoặc tài khoản break-glass.

Admin có thể cấu hình rule allow/deny theo segment thủ công, với deny thắng allow và kiểm tra bảo vệ học sinh vẫn áp dụng. Màn hình retention áp dụng số ngày lưu theo loại hội thoại hoặc toàn hệ thống. Khi bật retention, worker bắt đầu xóa nội dung đã quá hạn, cập nhật index và đưa tệp đính kèm vào hàng đợi dọn dẹp. Cơ sở dữ liệu giữ ID và seq của tin hết hạn để duy trì lịch sử thứ tự.

Các phần còn thiếu so với spec v1.2 gồm rule có điều kiện và phân quyền theo manager, đồng bộ segment theo rule, rà soát/hết hạn membership, lập chỉ mục file/user/channel, JetStream, push mobile, biểu thức audience, retention theo từng channel, presence/typing, bộ quan sát đầy đủ và cấu hình HA. API sử dụng PGX trực tiếp và client TypeScript viết tay trong bản này. Danh sách route hiện tại nằm trong [OpenAPI](../api/openapi.yaml), giao thức realtime trong [tài liệu WebSocket](../api/realtime/README.md). Stack Compose triển khai một instance server và worker. Cần hoàn thiện kiểm thử bảo mật, tải và khôi phục trước khi đưa dữ liệu học sinh vào production. Phiên này bỏ qua chạy kiểm thử theo yêu cầu. Compose parse thành công. Build và triển khai đầu cuối chưa được xác nhận trong môi trường làm việc vì thiếu dependency trong cache và truy cập Go module proxy bị chặn.
