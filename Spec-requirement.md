# Đặc tả yêu cầu: Ứng dụng chat nội bộ trường học (Backend Go)

Phiên bản 1.2 · 15/11/2026 · Dùng làm đầu vào cho AI coding

## 1. Bối cảnh và mục tiêu

Trường học cần một ứng dụng chat nội bộ **thay thế Microsoft Teams** cho phần nhắn tin. Hệ thống **self-host hoàn toàn**, xác thực **trực tiếp qua Active Directory (LDAPS)**, không phụ thuộc SSO/IdP bên ngoài.

**Quy mô:** \~11.000 tài khoản (khoảng 1.000 giáo viên/nhân viên và 10.000 học sinh), 2.000-3.000 kết nối đồng thời giờ cao điểm. Thiết kế chịu được gấp 3 lần.

**Đối tượng sử dụng:** không chỉ học sinh và giáo viên mà cả các phòng ban hành chính, vận hành (HR, tài chính, ban giám hiệu, IT...). Tất cả dùng chung một mô hình **segment + rule** (mục 7). Thành viên và manager của các phòng ban **không tự đồng bộ từ AD** nên được quản lý thủ công trong ứng dụng.

**Mục tiêu chính:**

- Chat 1-1, nhóm, channel theo lớp/môn/phòng ban
- Đăng nhập bằng tài khoản AD hiện có, tự đồng bộ user, group và channel membership
- Broadcast thỉnh thoảng tới toàn bộ \~10.000 học sinh, hoặc theo khối/lớp
- Bảo vệ học sinh (trẻ vị thành niên) bằng policy kiểm soát ai được nhắn tin cho ai
- Vận hành được bởi đội IT nhỏ: ít thành phần, dễ backup, dễ giám sát

## 2. Phạm vi

**Trong phạm vi (MVP):** chat, channel, thread, reaction, mention, file, search, AD auth, segment/policy (học sinh và các phòng ban), quản lý thành viên thủ công, broadcast, push notification, audit log, retention, admin API.

Chat dành cho iOS, Android và trình duyệt. Quản trị thực hiện trên ứng dụng web riêng.

**Ngoài phạm vi (giai đoạn sau):** gọi thoại/video, chia sẻ màn hình, bot/webhook/slash command, E2EE, workflow builder, tích hợp bên thứ ba.

**Giả định:** có ít nhất 2 domain controller hỗ trợ LDAPS; trường có tài khoản Apple Developer và Firebase project riêng; hạ tầng chạy on-premise bằng Docker.

## 3. Tech stack cố định

AI coding phải dùng đúng các lựa chọn dưới đây, không tự thay thế.

| Hạng mục | Lựa chọn |
| --- | --- |
| Ngôn ngữ | Go (phiên bản ổn định mới nhất) |
| HTTP router | `chi` |
| WebSocket | `github.com/coder/websocket` |
| DB access | `pgx` + `sqlc` (sinh code từ SQL) |
| Migration | `goose` |
| Database | PostgreSQL |
| Cache, presence, pub/sub | Redis (`go-redis`) |
| Queue | NATS JetStream (`nats.go`) |
| Search | OpenSearch (analyzer ICU/folding cho tiếng Việt có dấu) |
| Object storage | MinIO (`minio-go`), quét virus bằng ClamAV |
| LDAP | `github.com/go-ldap/ldap/v3` |
| JWT | `github.com/golang-jwt/jwt/v5` |
| Push | FCM (Firebase Admin SDK cho Go), APNs (`sideshow/apns2`) |
| Logging | `log/slog` (JSON) |
| Metrics/tracing | Prometheus + OpenTelemetry |
| Config | biến môi trường + file YAML, validate khi khởi động |
| Test | `testing`, `testify`, `testcontainers-go` |
| Đóng gói | Docker, docker-compose cho dev, k3s hoặc Compose/Swarm cho production |

## 4. Kiến trúc và cấu trúc repo

**Modular monolith**: một binary `server` (API + WebSocket) và một binary `worker` (push, broadcast, quét file, sync AD, indexing). Dùng chung codebase, tách bằng package. Không dùng microservices.

```
/cmd/server        # HTTP + WebSocket entrypoint
/cmd/worker        # queue consumers, scheduled jobs
/internal/auth     # LDAP, JWT, refresh token, break-glass admin
/internal/directory# đồng bộ AD (user, group, role mapping)
/internal/chat     # channel, message, thread, reaction, read state
/internal/realtime # WebSocket hub, presence, typing, resume
/internal/policy   # luật ai được nhắn ai, quyền theo role
/internal/broadcast# audience resolution, scheduling, stats
/internal/push     # device registry, FCM/APNs senders
/internal/files    # upload, MinIO, scan status
/internal/search   # indexer, query
/internal/audit    # append-only audit log
/internal/retention# job xóa/lưu trữ theo policy
/internal/platform # config, db, redis, nats, logger, metrics
/migrations        # goose SQL
/api               # openapi.yaml (nguồn chân lý của REST API)
/deploy            # docker-compose, k8s manifests
```

**Quy tắc kiến trúc:**

- Mỗi package `internal/*` chỉ lộ interface cần thiết; không import chéo vòng
- Mọi truy vấn SQL viết trong file `.sql` và sinh bằng `sqlc`
- Server **stateless**: trạng thái nằm ở Postgres/Redis, nên chạy được nhiều instance sau load balancer
- WebSocket giữa các instance đồng bộ qua Redis pub/sub
- `context.Context` truyền xuyên suốt, có timeout cho mọi lời gọi ra ngoài

### Monorepo và ranh giới triển khai

```text
backend/                     # Go module
  cmd/server/                # REST, xác thực, WebSocket
  cmd/worker/                # AD sync, outbox, search, file, push, retention
  internal/                  # các module nghiệp vụ ở trên
  migrations/
apps/
  mobile/                    # React Native + TypeScript, iOS và Android
  web-chat/                  # React + TypeScript, chat cho người dùng
  admin-web/                 # React + TypeScript, build tĩnh
packages/
  api-client/                # client và kiểu REST sinh từ OpenAPI
  realtime-contract/         # kiểu và kiểm tra payload WebSocket
api/
  openapi.yaml
  realtime/                  # JSON Schema và ví dụ WebSocket
deploy/
docs/
```

- Các đường dẫn `cmd`, `internal` và `migrations` trong sơ đồ backend thuộc `backend/`. Ba ứng dụng TypeScript dùng chung workspace và lockfile.
- **Mobile** chia theo auth, chat, files, search và notifications. Tầng dữ liệu quản lý REST, WebSocket, resume và hàng đợi gửi lại với `client_msg_id`. Refresh token lưu trong kho bảo mật hệ điều hành. Xóa dữ liệu chat cục bộ khi đăng xuất hoặc xác nhận phiên đã bị thu hồi.
- **Web quản trị** chia theo directory, segments, approvals, broadcasts, audit và retention. Reverse proxy phục vụ web tĩnh và chuyển tiếp `/v1` cùng origin. Access token giữ trong bộ nhớ. Refresh dùng cookie `HttpOnly`, `Secure`, `SameSite` và bảo vệ CSRF.
- **Web chat** chia theo auth, channels, messages, threads, files, search và announcements. Build tĩnh trên origin riêng với web quản trị. Reverse proxy origin web chat phục vụ frontend và chuyển tiếp REST/WebSocket `/v1` tới cùng backend.
- Client dùng chung hợp đồng, kiểu dữ liệu và client API. Backend thực thi policy và audit. Mỗi app sở hữu UI và trạng thái riêng. CI kiểm tra mã sinh khớp hợp đồng và build/test từng app. Server, worker, web và mobile có quy trình phát hành riêng. Duy trì tương thích `/v1` với các bản mobile còn hỗ trợ.

## 5. Mô hình dữ liệu

ID dùng **ULID** (text 26 ký tự) cho `channels`, `messages`, `files`; UUID cho các bảng còn lại. Mọi bảng có `created_at`; bảng có thể sửa có `updated_at`.

- **users**: id, ad\_guid (unique), username, display\_name, email, status (`active|disabled`), org\_unit, grade (smallint, null), class\_code (null), manager\_id (null, nhập thủ công, tùy chọn), is\_system\_admin (bool), avatar\_key, last\_synced\_at. Quyền **không** lưu bằng cột role cố định mà tính từ segment (mục 7)
- **ad\_groups** / **user\_ad\_groups**: dn, name; quan hệ nhiều-nhiều với users
- **segments**: id, key (unique, ví dụ `student:grade10`, `dept:hr`), name, mode (`ad|manual|hybrid`), ad\_group\_dn (null, dùng cho `ad` và `hybrid`), is\_minor (bool), sensitive (bool), review\_interval\_days (null)
- **segment\_members**: segment\_id, user\_id, role (`member|manager|owner`), source (`ad_sync|manual`), suppressed (bool), added\_by, added\_at, expires\_at (null), last\_reviewed\_at
- **segment\_change\_requests**: id, segment\_id, action (`add|remove|set_role`), target\_user\_id, requested\_by, approved\_by (null), status (`pending|approved|rejected`); dùng cho segment `sensitive`
- **segment\_reviews**: id, segment\_id, due\_at, completed\_by (null), completed\_at (null), removed\_count
- **policy\_rules**: id, subject\_segment, action, object\_segment (null), scope (jsonb), effect (`allow|deny`), conditions (jsonb), priority (int), enabled
- **channels**: id, type (`public|private|dm|group_dm|announcement`), name, topic, class\_code (null), segment\_id (null, channel gắn segment), created\_by, archived\_at, **last\_seq bigint**
- **channel\_members**: channel\_id, user\_id, role (`owner|member`), **last\_read\_seq**, muted\_until, notif\_pref (`all|mentions|none`), source (`manual|segment`), joined\_at
- **messages**: id, channel\_id, **seq bigint**, sender\_id, parent\_id (null, dùng cho thread), body, attachments (jsonb), edited\_at, deleted\_at. Bảng thường trong MVP, PRIMARY KEY (id), UNIQUE (channel\_id, seq). Đánh giá partition khi có số liệu vận hành.
- **reactions**: message\_id, user\_id, emoji
- **files**: id, uploader\_id, channel\_id, object\_key, name, mime, size, sha256, scan\_status (`pending|clean|infected`)
- **devices**: id, user\_id, platform (`ios|android|web`), push\_token, app\_version, last\_seen\_at
- **refresh\_tokens**: id, user\_id, device\_id, token\_hash, expires\_at, revoked\_at, replaced\_by
- **broadcasts**: id, created\_by, audience (jsonb, biểu thức segment), message\_id, scheduled\_at, status (`draft|scheduled|sending|done|failed|cancelled`), stats (jsonb)
- **push\_jobs**: id, broadcast\_id (null), device\_id, status, attempts, last\_error
- **policies**: key, value (jsonb), updated\_by (cấu hình chung như giờ yên lặng, danh sách từ khóa cấm)
- **retention\_rules**: scope (`global|channel_type|channel`), scope\_ref, keep\_days
- **audit\_logs**: id, actor\_id, action, target\_type, target\_id, metadata (jsonb), ip, created\_at. Chỉ INSERT (revoke UPDATE/DELETE ở mức DB role)

**Cấp seq (bắt buộc đúng):** khi gửi tin, trong **một transaction** thực hiện `UPDATE channels SET last_seq = last_seq + 1 WHERE id = $1 RETURNING last_seq`, rồi INSERT message với seq đó. Seq liên tục, không trùng, không nhảy cóc trong một channel.

**Đã đọc:** cập nhật `last_read_seq` theo mốc lớn nhất đã xác nhận, giới hạn bởi `last_seq`. Unread đếm tin còn hiển thị, có quyền đọc, với `seq > last_read_seq`. Mốc đọc ban đầu phụ thuộc quyền xem lịch sử khi vào channel.

## 6. Xác thực AD và phiên đăng nhập

**Đăng nhập** `POST /v1/auth/login` với `username`, `password`, `device` (platform, push\_token tùy chọn):

1. Kết nối LDAPS tới danh sách DC (thử lần lượt, timeout ngắn, có circuit breaker)
2. Bind bằng service account read-only, tìm user theo `sAMAccountName` hoặc `userPrincipalName`
3. Bind lại bằng DN của user với mật khẩu cung cấp để xác thực
4. Đọc `memberOf`, `userAccountControl`, `objectGUID`; map group AD → **segment** (chỉ các segment `mode=ad` hoặc `hybrid`) theo cấu hình YAML (ví dụ group `CN=HocSinh` → segment `student`). Segment `manual` không phụ thuộc AD
5. Upsert user vào DB, đồng bộ membership `source=ad_sync`, cấp **access JWT (15 phút)** và **refresh token (30 ngày, xoay vòng, lưu hash)**

**Yêu cầu:**

- Mật khẩu không bao giờ được log hay lưu
- Refresh **không** gọi AD, nên phiên đang đăng nhập không chết khi AD gián đoạn
- Job sync mỗi 15 phút: user bị disable/xóa trong AD → `status=disabled`, thu hồi toàn bộ refresh token, ngắt WebSocket
- Rate limit đăng nhập theo IP và theo username (Redis)
- **Break-glass admin:** 1-2 tài khoản local (mật khẩu hash bằng argon2id) để vào được hệ thống khi AD sập. Mọi lần dùng đều ghi audit
- Phát hiện reuse refresh token cũ → thu hồi cả chuỗi token của thiết bị đó

**Thu hồi phiên:** REST kiểm tra trạng thái user và phiên qua store dùng chung. WebSocket kiểm tra khi mở, refresh và trước khi xử lý hoặc phát dữ liệu được bảo vệ. Khóa tài khoản vô hiệu phiên sau commit, đóng socket trong tối đa 60 giây. Lưu khóa thủ công riêng với trạng thái AD. Chỉ suy ra user bị xóa sau một vòng đối soát hoàn tất thành công. MVP ánh xạ group trực tiếp. Dùng OpenLDAP trong CI và kiểm thử LDAPS, objectGUID, disable và failover trên AD staging.

## 7. Phân quyền, segment và policy

Quyền tính từ **segment** và **rule**. Mặc định từ chối. Mọi deny khớp thắng mọi allow khớp. `priority` chỉ sắp xếp rule cùng hiệu lực để giải thích quyết định. Ngoại lệ DM theo khối cần điều kiện loại trừ trong rule deny và rule allow tường minh. Kiểm tra lại quyền khi đọc, gửi, đổi thành viên hoặc phát realtime. Group DM mất giáo viên cuối cùng bị khóa gửi tới khi đủ điều kiện.

**Segment.** Mỗi segment có `key` (ví dụ `student`, `student:grade10`, `teacher`, `staff`, `dept:hr`, `dept:finance`, `management`), `mode` (`ad`: chỉ sync từ group AD; `manual`: chỉ quản lý trong app; `hybrid`: cả hai), cờ `is_minor` (segment học sinh, tự áp policy bảo vệ mặc định) và cờ `sensitive` (HR, tài chính, ban giám hiệu: bật duyệt hai người và rà soát chặt hơn). Một người thuộc nhiều segment (giáo viên vừa là `teacher` vừa là `dept:math`); quyền hiệu lực là hợp của mọi segment, tính khi đăng nhập, cache trong Redis và xóa cache khi membership đổi.

**Rule mẫu mặc định** (cấu hình được trong `policy_rules`):

| Subject | Action | Object | Effect |
| --- | --- | --- | --- |
| `student` | `dm.create` | `student` | deny (mặc định, bật được theo khối) |
| `student` | `dm.create` | `teacher` | allow |
| `student` | `channel.create` | mọi loại | deny |
| mọi segment | `group_dm.create` | nhóm có thành viên `student` | allow khi có ít nhất 1 thành viên `teacher` |
| `teacher` | `channel.create` | mọi loại | allow |
| `teacher` | `broadcast.send` | lớp phụ trách | allow |
| `staff` | `dm.create` | `staff` | allow |
| `dept:hr` | `channel.create` | private | allow |
| `management` | `broadcast.send` | mọi audience | allow |
| manager của `dept:X` | `broadcast.send` | `dept:X` | allow |
| manager của `dept:X` | `segment.member.manage` | `dept:X` | allow |
| mọi segment | `message.delete_others` | channel mình sở hữu | allow |
| `student`, `guest` | `broadcast.send`, `audit.read` | mọi audience | deny |

**Quản lý thành viên thủ công và yêu cầu chung:** danh tính luôn lấy từ AD, nhưng thành viên và manager của phòng ban không tự đồng bộ nên được quản lý trong ứng dụng.

- Segment `manual` hoặc `hybrid`: membership `source=manual` do owner/manager (được ủy quyền) hoặc admin hệ thống thêm/xóa. Job sync AD **chỉ thao tác trên dòng `source=ad_sync`**, không bao giờ xóa hay ghi đè dòng `manual`
- Muốn loại một người dù AD vẫn cho vào group thì đặt `suppressed=true` thay vì xóa dòng, để lần sync sau không thêm lại
- Vai trò: `member`; `manager` (thêm/xóa member, duyệt yêu cầu tham gia trong segment của mình); `owner` (thêm được cả manager khác). Chỉ admin hệ thống được tạo segment mới và gán owner đầu tiên
- Thành viên tạm thời bằng `expires_at`; job hằng ngày gỡ thành viên hết hạn
- **Rà soát định kỳ** (mặc định 6 tháng, theo `review_interval_days`): hệ thống nhắc manager xác nhận danh sách; thành viên không được xác nhận sau hạn bị gỡ hoặc báo cho admin (cấu hình được)
- Segment `sensitive`: mọi thay đổi membership đi qua `segment_change_requests` và cần người thứ hai duyệt
- Offboarding: user bị disable hoặc xóa trong AD tự bị gỡ khỏi mọi segment (kể cả membership thủ công) và bị thu hồi phiên
- Channel gắn segment (`channels.segment_id`): khi membership đổi, `channel_members` tự cập nhật với `source=segment`; thành viên thêm tay vào channel giữ `source=manual` và không bị sync xóa
- Segment `is_minor` tự áp bộ rule bảo vệ mặc định: cấm DM giữa học sinh, group DM có học sinh bắt buộc có giáo viên, giờ yên lặng (không push ngoài khung giờ, tin vẫn được lưu), lọc từ khóa cấu hình được, báo cáo tin nhắn cho giáo viên/admin
- Cô lập dữ liệu là **tùy chọn theo segment, mặc định tắt**: khi bật cho segment `sensitive`, chỉ thành viên mới thấy channel và tin (kể cả trong search); admin hệ thống chỉ đọc nội dung khi được cấp quyền riêng và mọi lần đọc ghi audit
- Mọi kiểm tra quyền nằm ở `internal/policy` (hàm thuần, dễ test), được gọi từ cả REST lẫn WebSocket; mọi thay đổi membership và rule đều ghi audit

**Duyệt hai người:** segment nhạy cảm dùng mode `manual` trong MVP. Thay đổi thủ công, kể cả CSV và đổi vai trò, cần người có quyền duyệt khác người yêu cầu. Hết hạn và offboarding chặn quyền ngay. Ghi audit trong cùng transaction khi thay đổi membership, duyệt hoặc từ chối.

## 8. REST API

Tiền tố `/v1`. Nguồn chân lý là `api/openapi.yaml`; AI phải viết file này **trước** khi cài đặt handler. Xác thực bằng `Authorization: Bearer <JWT>`. Lỗi trả về dạng `{ "error": { "code", "message" } }`. Danh sách dùng **cursor pagination**.

**Auth:** `POST /auth/login`, `POST /auth/refresh`, `POST /auth/logout`, `GET /me`, `PUT /me/devices` (đăng ký/cập nhật push token)

OpenAPI mô tả refresh token trong body cho mobile và refresh cookie giới hạn theo host cho web chat và web quản trị. Web nhận cookie từ login và refresh, logout thu hồi phiên và xóa cookie. Mỗi bề mặt từ chối request refresh chứa đồng thời token body và cookie.

**Users và directory:** `GET /users` (tìm theo tên, lớp, khối), `GET /users/{id}`, `GET /users/{id}/presence`

**Channels:**

- `GET /channels` (channel của tôi, kèm unread), `POST /channels`, `GET /channels/{id}`, `PATCH /channels/{id}`, `POST /channels/{id}/archive`
- `GET/POST/DELETE /channels/{id}/members`
- `POST /dms` (mở hoặc lấy DM, kiểm tra policy)
- `POST /channels/{id}/read` với `{ "seq": N }` (cập nhật last\_read\_seq)
- `PUT /channels/{id}/prefs` (mute, notif\_pref)

**Messages:**

- `GET /channels/{id}/messages?before_seq=&after_seq=&limit=` (mặc định 50, tối đa 100)
- `POST /channels/{id}/messages` với `body`, `parent_id`, `attachment_ids`, `client_msg_id` (**idempotency key**: gửi lại cùng `client_msg_id` không tạo tin trùng)
- `PATCH /messages/{id}`, `DELETE /messages/{id}` (xóa mềm)
- `PUT/DELETE /messages/{id}/reactions/{emoji}`
- `GET /messages/{id}/thread`
- `POST /messages/{id}/report`

**Files:** `POST /files` (multipart hoặc presigned URL), `GET /files/{id}` (chỉ trả khi `scan_status=clean` và người dùng có quyền vào channel), giới hạn dung lượng và loại file cấu hình được

**Search:** `GET /search?q=&type=messages|files|users|channels&channel_id=&from=&to=`. Kết quả luôn **lọc theo quyền** của người gọi

**Broadcast:** `POST /broadcasts`, `GET /broadcasts`, `GET /broadcasts/{id}` (kèm thống kê), `POST /broadcasts/{id}/cancel`

**Segments:** `GET/POST /segments`, `PATCH /segments/{id}`, `GET /segments/{id}/members`, `POST /segments/{id}/members`, `PATCH/DELETE /segments/{id}/members/{user_id}` (đổi vai trò, đặt hạn, gỡ), `POST /segments/{id}/members/import` (CSV), `POST /segments/{id}/review` (xác nhận rà soát), `GET /segment-change-requests`, `POST /segment-change-requests/{id}/approve` và `/reject`. Mọi endpoint kiểm tra quyền theo vai trò của người gọi trong segment

**Admin:** `GET/PUT /admin/policies`, `GET/POST/PUT/DELETE /admin/policy-rules`, `GET/PUT /admin/retention`, `GET /admin/audit`, `POST /admin/directory/sync`, `GET /admin/reports`, `POST /admin/users/{id}/disable`

**Idempotency gửi tin:** lưu khóa duy nhất theo `(channel_id, sender_id, client_msg_id)`, fingerprint payload và ID tin trong giao dịch cấp seq. Retry đồng thời cùng payload trả cùng ID và seq; khác payload trả `409`. Giữ khóa tối thiểu 7 ngày kể cả khi tin bị xóa.

## 9. Giao thức WebSocket

Endpoint `GET /v1/ws`; mobile gửi `Authorization: Bearer <JWT>` khi mở kết nối. Web chat mở WSS cùng origin và gửi access token trong frame `auth` đầu tiên. Server kiểm tra `Origin` theo allowlist khi upgrade từ trình duyệt, giới hạn kết nối chưa xác thực, đóng sau 5 giây nếu chưa xác thực, và chỉ cho đăng ký luồng hoặc gửi dữ liệu sau `auth.ok`. Token truyền trong header hoặc frame `auth`; các giá trị này phải được che khỏi log. JSON frame dạng `{ "type", "id", "data" }`.

**Client → server:** `ping`, `typing` (channel\_id), `presence.set`, `auth`, `resume` (gửi `{ channel_id: last_event_cursor }` cho từng channel đang mở)

**Server → client:** `auth.ok`, `auth.expiring`, `pong`, `message.new`, `message.updated`, `message.deleted`, `reaction.changed`, `typing`, `presence.changed`, `channel.updated`, `member.changed`, `read.updated`, `broadcast.new`

**Yêu cầu:**

- Heartbeat 25 giây; server đóng kết nối nếu quá 60 giây không có tín hiệu
- **Resume:** lưu cursor và dữ liệu sự kiện bền vững trong transaction khởi tạo, sửa, xóa tin hoặc đổi reaction. Worker dùng transactional outbox để publish và retry với cùng ID sự kiện. Khi resume, server đệm luồng trực tiếp, replay đến mốc đã chốt rồi hợp nhất theo cursor và loại trùng. Khi Redis kết nối lại, các socket bị ảnh hưởng phải bắt kịp. Cursor hết hạn yêu cầu tải lại snapshot. Presence và typing chỉ phát trực tiếp.
- Token hết hạn trong lúc đang kết nối: server gửi `auth.expiring`, client refresh và gửi lại token qua frame `auth`
- Giới hạn tốc độ gửi tin theo user (ví dụ 20 tin/10 giây); vượt thì trả lỗi và không ngắt kết nối
- Mỗi instance giữ hub trong bộ nhớ, publish/subscribe qua Redis để phát tới người dùng nằm ở instance khác
- Presence lưu trong Redis với TTL; typing không lưu DB

## 10. Broadcast tới 10.000 học sinh

**Nguyên tắc:** lưu **một bản ghi tin nhắn** trong announcement channel; **không** nhân bản tin cho từng học sinh. Chỉ phần push là fan-out.

**Luồng:**

1. Người có quyền gọi `POST /broadcasts` với `audience` là **biểu thức segment** (ví dụ `student:grade10`, `dept:hr`, hoặc `student:grade10 AND NOT student:class10A3`), tùy chọn `scheduled_at`
2. Server kiểm tra rule `broadcast.send` và phạm vi audience của người gửi (manager `dept:finance` chỉ gửi tới `dept:finance`; giáo viên chỉ gửi tới lớp phụ trách nếu rule yêu cầu)
3. Khi đến giờ, worker kiểm tra quyền, phân giải audience và commit snapshot người nhận, tin trong announcement channel và sự kiện outbox cùng một transaction. Mỗi broadcast chỉ tạo một tin qua ràng buộc duy nhất. Phát `broadcast.new` sau commit.
4. Worker lấy device từ snapshot người nhận, ghi `push_jobs`, đẩy vào NATS, gửi theo lô (FCM tối đa 500 token/lần; APNs song song có giới hạn concurrency), có **rate limit toàn cục** và retry exponential backoff
5. Token không hợp lệ (unregistered) bị xóa khỏi `devices`
6. Thống kê: tổng người nhận, số push thành công/thất bại, số người xác nhận đọc bằng `PUT /broadcasts/{id}/read` idempotent.

**Quyền đọc broadcast:** kiểm tra phạm vi audience lúc tạo và quyền người gửi lúc thực thi. Chốt snapshot người nhận khi bắt đầu gửi. Lưu một tin và quan hệ broadcast–user. Feed, WebSocket, search, file và push đều kiểm tra quan hệ này, trạng thái tài khoản và điều kiện cô lập dữ liệu hiện hành. Thành viên mới không nhận broadcast cũ. Job push dùng khóa duy nhất theo broadcast/device và kiểm tra trạng thái hủy trước mỗi lô. Retry có thể gửi lại push nếu worker chết sau khi provider chấp nhận nhưng trước khi lưu kết quả.

**Yêu cầu hiệu năng:** push tới 10.000 thiết bị hoàn tất trong dưới 2 phút, **không** làm tăng độ trễ gửi tin thường quá 20%. Có thể hủy broadcast khi đang gửi (các job chưa gửi bị bỏ).

## 11. Push notification

- Đăng ký thiết bị qua `PUT /me/devices`; một user nhiều thiết bị
- Quy tắc gửi push: không gửi nếu user đang online ở thiết bị khác và đã xem, đang mute/DND, ngoài giờ yên lặng, hoặc `notif_pref` không khớp (`mentions` chỉ gửi khi có @mention)
- **Nội dung push tối giản** (ví dụ tên người gửi và "Có tin nhắn mới"), chi tiết lấy từ server khi mở app; cấu hình được để hiện/ẩn nội dung
- Gộp (collapse) push cùng channel để tránh spam
- Thông tin chứng chỉ APNs và service account FCM nạp từ secret, không commit vào repo

## 12. File

- Upload lên MinIO; server lưu metadata và tạo job quét ClamAV; file ở trạng thái `pending` thì chưa tải được
- File `infected` bị xóa và ghi audit, thông báo cho người gửi
- Kiểm tra MIME bằng nội dung thật (không chỉ phần mở rộng), giới hạn mặc định 50 MB/file, danh sách loại file cho phép cấu hình được
- Link tải là presigned URL ngắn hạn, cấp sau khi kiểm tra quyền
- Tạo thumbnail cho ảnh

## 13. Search

- Worker lập chỉ mục tin nhắn, file (tên), user, channel vào OpenSearch gần thời gian thực (độ trễ dưới 5 giây)
- Analyzer hỗ trợ **tiếng Việt có dấu và không dấu** (ICU folding), tìm theo tiền tố
- Mỗi document mang danh sách channel/quyền; **kết quả luôn lọc theo membership** của người tìm
- Tin bị xóa hoặc hết retention phải bị xóa khỏi index
- Có lệnh reindex toàn bộ từ Postgres

## 14. Retention, audit và dữ liệu

- **Retention:** job hằng ngày xóa hoặc lưu trữ tin theo `retention_rules` (mặc định theo loại channel; cuối năm học có thao tác archive channel lớp)
- **Audit log** ghi tối thiểu: đăng nhập thành công/thất bại, break-glass, thay đổi policy rule, **mọi thay đổi membership và vai trò segment (kể cả do sync AD)**, duyệt/từ chối yêu cầu thay đổi, rà soát định kỳ, xóa tin bởi người khác, thêm/xóa thành viên channel thủ công, broadcast, xuất dữ liệu, vô hiệu hóa user. Không sửa hoặc xóa được qua ứng dụng
- **Backup:** Postgres (base backup + WAL), MinIO; có script restore và kiểm thử khôi phục định kỳ
- Dữ liệu cá nhân của học sinh chỉ lưu trong hạ tầng của trường; ghi rõ trong tài liệu vận hành các điểm dữ liệu đi ra ngoài (metadata push qua APNs/FCM)

## 15. Yêu cầu phi chức năng

| Hạng mục | Mục tiêu |
| --- | --- |
| Độ trễ gửi tin (p95) | dưới 300 ms trong mạng nội bộ |
| Kết nối đồng thời | 3.000 ổn định, kiểm thử tải tới 10.000 |
| Availability | 99,9%; Postgres có replica và failover; server chạy tối thiểu 2 instance |
| Bảo mật | TLS mọi nơi, OWASP ASVS L2 làm chuẩn, validate toàn bộ input, parameterized query, CORS chặt, secure headers, dependency scan trong CI |
| Mã hóa lưu trữ | Mã hóa ổ đĩa/volume; secret nằm ngoài repo |
| Quan sát | Metrics (số kết nối WS, độ trễ gửi tin, kích thước queue, lỗi push), log có `request_id`, trace OpenTelemetry, dashboard Grafana, alert cơ bản |
| Đa ngôn ngữ | Thông báo lỗi và nội dung hệ thống hỗ trợ Việt/Anh |
| Múi giờ | Lưu UTC, hiển thị theo múi giờ client (Asia/Ho\_Chi\_Minh mặc định) |

## 16. Kiểm thử và tiêu chí chất lượng

- **Unit test** cho `policy`, cấp seq, phân giải audience, mapping group → role (coverage từ 80% với các package này)
- **Integration test** với `testcontainers-go` (Postgres, Redis, NATS, MinIO, một LDAP server giả như OpenLDAP); mọi endpoint có ít nhất một test thành công và một test bị từ chối quyền
- **Test realtime:** mất kết nối giữa chừng rồi resume, đảm bảo không mất và không trùng tin; gửi trùng `client_msg_id`
- **Test tải** (k6 hoặc `vegeta`): 3.000 WebSocket đồng thời với tải tin nhắn thực tế; kịch bản broadcast 10.000 thiết bị với FCM/APNs giả (mock)
- **Test bảo mật:** truy cập chéo channel (IDOR), search lộ tin ngoài quyền, token đã thu hồi, brute-force đăng nhập
- CI bắt buộc: `go vet`, `golangci-lint`, test, `govulncheck`, build Docker image

Kiểm thử tải ở cả 3.000 và 10.000 WebSocket với tải gửi, kích thước channel và cấu hình môi trường được ghi lại. Đo p95 từ lúc API nhận request đến lúc trả kết quả commit. So sánh p95 có và không broadcast trên cùng tải. Mốc hai phút tính từ lúc worker bắt đầu đến lúc toàn bộ job được provider mock chấp nhận hoặc kết thúc với lỗi. Ngưỡng phục hồi ban đầu là RPO 5 phút, RTO 60 phút, kiểm tra bằng diễn tập khôi phục Postgres và MinIO. Thêm test cho retry đồng thời, thu hồi quyền giữa phiên, cô lập broadcast theo người nhận và mất Redis khi WebSocket vẫn mở.

## 17. Lộ trình và tiêu chí nghiệm thu theo milestone

Mỗi milestone phải **chạy được, có test, có tài liệu** trước khi sang milestone kế.

**M0 - Khung dự án:** repo, cấu trúc thư mục, `docker-compose` dev (Postgres, Redis, NATS, MinIO, OpenSearch, OpenLDAP), migration đầu tiên, CI, `openapi.yaml` toàn bộ MVP. *Nghiệm thu:* `docker compose up` rồi `make test` chạy xanh.

**M1 - Auth và directory:** đăng nhập LDAPS, JWT + refresh xoay vòng, break-glass admin, sync AD, map group → segment. *Nghiệm thu:* đăng nhập bằng user OpenLDAP thử nghiệm; disable user trong LDAP thì phiên bị thu hồi sau lần sync kế.

**M2 - Chat lõi:** channel, DM, thành viên, tin nhắn, seq, thread, reaction, đã đọc, segment và policy engine (rule allow/deny), quản lý thành viên segment thủ công. *Nghiệm thu:* học sinh không DM được học sinh khác khi policy cấm; manager phòng ban chỉ thêm/xóa được thành viên segment của mình; sync AD không xóa thành viên thêm thủ công; gửi trùng `client_msg_id` không tạo tin thứ hai.

**M3 - Realtime:** WebSocket hub, presence, typing, resume, Redis pub/sub giữa 2 instance. *Nghiệm thu:* ngắt mạng client 30 giây rồi nối lại vẫn nhận đủ tin, đúng thứ tự.

**M4 - File và search:** upload, quét ClamAV, thumbnail, OpenSearch, lọc theo quyền. *Nghiệm thu:* file nhiễm virus bị chặn; user ngoài channel không tìm thấy tin của channel đó.

**M5 - Push và broadcast:** device registry, FCM/APNs sender, announcement channel, broadcast theo biểu thức segment (khối, lớp, phòng ban), lên lịch, thống kê. *Nghiệm thu:* broadcast tới 10.000 device giả dưới 2 phút.

**M6 - Vận hành và hardening:** audit, retention, admin API, rà soát định kỳ thành viên segment, metrics, backup/restore, test tải, rà soát bảo mật, tài liệu vận hành. *Nghiệm thu:* đạt các chỉ tiêu ở mục 15 và 16.

**Mốc tích hợp ứng dụng:** M0 dựng skeleton ba app và quy trình sinh client API. M1 nghiệm thu đăng nhập trên cả ba app. M2–M3 tích hợp chat/realtime trên mobile và web chat, cùng quản lý segment trên web quản trị. M4–M5 tích hợp file/search và nhận broadcast trên cả hai app chat, push trên mobile và gửi broadcast trên web quản trị. M6 hoàn tất các màn hình vận hành và kiểm thử web chat theo mục 18. Audit bắt buộc ngay tại milestone phát sinh thao tác. Ứng dụng desktop native thuộc giai đoạn sau.

## 18. Đặc tả web chat

**Đối tượng và phạm vi.** Học sinh, giáo viên và nhân viên dùng `apps/web-chat` để trò chuyện trong trình duyệt. MVP hỗ trợ các bản ổn định Chrome, Edge, Firefox và Safari trên desktop tại thời điểm nghiệm thu. Giao diện responsive dùng được trên màn hình hẹp. Web Push, PWA và dùng ngoại tuyến qua lần tải lại trang thuộc giai đoạn sau. Thông báo trên web MVP hiển thị trong ứng dụng khi tab đang mở.

### Luồng người dùng

- Đăng nhập AD, khôi phục phiên khi tải lại trang, đăng xuất. Danh sách channel và DM hiển thị unread, mute và trạng thái kết nối. Tìm người trong directory và mở DM hoặc group DM qua API theo policy.
- Timeline tải theo cursor, hỗ trợ tải lịch sử, nhảy tới tin được liên kết từ tìm kiếm hoặc thông báo. Soạn tin, @mention, sửa/xóa theo quyền, trả lời thread, reaction và báo cáo tin. Dùng cùng quy tắc unread với mobile. Chỉ xác nhận đọc khi cuộc trò chuyện đang hiển thị và timeline đã tới mốc tương ứng.
- Tải tệp lên, hiển thị trạng thái quét và kết quả bị từ chối. Chỉ cung cấp thao tác tải về cho file sạch qua API. Hiển thị kết quả tìm kiếm, announcement theo audience và xác nhận đọc từng broadcast khi mở nội dung.
- Hỗ trợ Việt/Anh, thao tác bằng bàn phím, focus hiển thị và nhãn cho trình đọc màn hình. Có trạng thái tải, rỗng, mất kết nối, gửi lỗi, hết phiên và mất quyền truy cập.

### Phiên, realtime và dữ liệu trên trình duyệt

- Access token và dữ liệu chat chỉ giữ trong bộ nhớ. Phiên trình duyệt dùng refresh cookie giới hạn theo host, `HttpOnly`, `Secure`, `SameSite`. Kiểm tra Origin và CSRF khi endpoint dùng cookie. Đồng bộ đăng xuất giữa tab và tuần tự hóa refresh giữa các tab cùng origin để tránh kích hoạt thu hồi chuỗi token do refresh đồng thời.
- Dùng WebSocket xác thực và resume theo mục 9, với backoff khi mất kết nối. Lưu cursor sự kiện trong bộ nhớ, loại trùng sau replay, tải lại snapshot khi cursor quá hạn. Tin chờ gửi được giữ trong tab và retry với cùng `client_msg_id`. Khi tải lại trang, timeline lấy lại trạng thái đã commit từ server.
- Escape nội dung do người dùng nhập và sanitize nếu có render rich text. Khi đăng xuất, phiên bị thu hồi hoặc user mất quyền channel, đóng kết nối hoặc hủy đăng ký tương ứng, xóa dữ liệu liên quan khỏi bộ nhớ và dừng retry. API trả nội dung riêng tư với `Cache-Control: no-store`.
- UI dựa trên quyền backend trả về để hiển thị thao tác và xử lý `401/403` từ REST hoặc WebSocket. Route và đường dẫn trực tiếp tới channel, tin, search và file đều chịu cùng kiểm tra quyền phía server.

### Nghiệm thu

- Hai trình duyệt đăng nhập, gửi và sửa tin, mở thread và đồng bộ unread. Ngắt mạng một tab trong 30 giây, gửi tin ở tab khác rồi nối lại. Timeline bắt kịp đúng thứ tự, cập nhật tin đã sửa và không tạo tin trùng khi retry.
- Hai tab cùng hết hạn access token vẫn giữ được phiên hợp lệ khi refresh. Đăng xuất từ một tab sẽ kết thúc phiên ở các tab còn lại cùng origin. WebSocket từ origin ngoài allowlist hoặc chưa xác thực không nhận dữ liệu.
- Gỡ quyền channel khi người dùng đang mở timeline. UI xóa nội dung liên quan và API/WS từ chối đọc tiếp. Tìm kiếm và liên kết trực tiếp cũng không truy cập được nội dung. Broadcast chỉ hiển thị cho người nhận hợp lệ, file đang chờ quét không tải được.
- Kiểm thử các luồng trên nhóm trình duyệt hỗ trợ, kèm kiểm tra bằng bàn phím cho đăng nhập, danh sách channel, soạn tin và thao tác gửi ở bố cục desktop và màn hình hẹp.

## 19. Cách dùng tài liệu này với AI coding

1. Đưa AI **toàn bộ tài liệu** làm bối cảnh cố định (hoặc lưu thành `CLAUDE.md`/`SPEC.md` trong repo), kèm lệnh: *"Tuân thủ tech stack và cấu trúc ở mục 3-4, không tự đổi công nghệ."*
2. Yêu cầu làm **từng milestone một**, theo thứ tự M0 → M6, mỗi lần một nhóm tính năng nhỏ (một package hoặc một nhóm endpoint)
3. Với mỗi bước, bắt AI **viết test trước hoặc cùng lúc**, chạy test và sửa đến khi xanh trước khi sang bước sau
4. Luôn bắt đầu bằng `openapi.yaml` và migration, review thủ công hai thứ này kỹ nhất vì mọi thứ khác phụ thuộc vào chúng
5. **Con người phải review kỹ** các phần: xác thực/LDAP, policy phân quyền, truy vấn search lọc quyền, xử lý token, phát sinh seq, và mọi chỗ xử lý dữ liệu học sinh
6. Sau mỗi milestone chạy lint, test, `govulncheck` và một lượt review bảo mật riêng (nhờ AI rà soát theo checklist OWASP ASVS, nhưng không thay cho review của người)
7. Ghi các quyết định thiết kế phát sinh vào `docs/decisions.md` để AI giữ nhất quán ở các phiên sau
