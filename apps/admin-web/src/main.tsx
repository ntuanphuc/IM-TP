import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { api, ApiError, type User } from "../../../packages/api-client/session";
import { Session } from "../../../packages/api-client/session-view";
import "../../../packages/api-client/styles.css";

type RecordRow = Record<string, string | boolean | number | null>;
type View = "users" | "segments" | "requests" | "broadcasts" | "reports" | "audit" | "retention" | "policy-rules";
const labels: Record<View, string> = {
  users:"Danh bạ", segments:"Segment", requests:"Phê duyệt",
  broadcasts:"Thông báo", reports:"Báo cáo", audit:"Nhật ký", retention:"Lưu giữ dữ liệu", "policy-rules":"Luật phân quyền",
};
const columns: Record<View, [string,string][]> = {
  users:[["username","Tài khoản"],["display_name","Tên"],["disabled","Khóa thủ công"],["ad_disabled","Khóa AD"],["admin","Quản trị"]],
  segments:[["key","Mã"],["name","Tên"],["sensitive","Nhạy cảm"]],
  requests:[["segment_name","Segment"],["username","Tài khoản"],["role","Thay đổi"],["status","Trạng thái"]],
  broadcasts:[["segment_name","Segment"],["body","Nội dung"],["scheduled_at","Lịch gửi"],["status","Trạng thái"],["recipients","Người nhận"],["readers","Đã đọc"]],
  reports:[["message_id","Tin nhắn"],["reason","Lý do"],["created_at","Thời điểm"]],
  audit:[["action","Hành động"],["actor_id","Người thực hiện"],["target","Đối tượng"],["created_at","Thời điểm"]],
  retention:[["scope","Phạm vi"],["keep_days","Số ngày lưu"],["updated_at","Cập nhật"]],
  "policy-rules":[["action","Hành động"],["effect","Hiệu lực"],["subject","Segment nguồn"],["object","Segment đích"],["enabled","Đang bật"]],
};

function Table({rows, fields, action}: {rows:RecordRow[]; fields:[string,string][]; action?:(row:RecordRow)=>ReactNode}) {
  if (rows.length === 0) return <p>Chưa có dữ liệu.</p>;
  return <div className="table"><table>
    <thead><tr>{fields.map(([key,title]) => <th key={key} scope="col">{title}</th>)}{action && <th scope="col">Thao tác</th>}</tr></thead>
    <tbody>{rows.map((row) => <tr key={String(row.id ?? row.user_id)}>
      {fields.map(([key]) => <td key={key}>{typeof row[key] === "boolean" ? (row[key] ? "Có" : "Không") : String(row[key] ?? "—")}</td>)}
      {action && <td>{action(row)}</td>}
    </tr>)}</tbody>
  </table></div>;
}

function Admin({user}: {user:User}) {
  const [view, setView] = useState<View>("users");
  const [rows, setRows] = useState<RecordRow[]>([]);
  const [segments, setSegments] = useState<RecordRow[]>([]);
  const [segment, setSegment] = useState("");
  const [members, setMembers] = useState<RecordRow[]>([]);
  const [people, setPeople] = useState<RecordRow[]>([]);
  const [query, setQuery] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const report = useCallback((failure:unknown) => {
    setError(failure instanceof ApiError ? "Thao tác thất bại (" + failure.code + ")." : "Không thể kết nối máy chủ.");
  }, []);

  useEffect(() => {
    let active = true;
    api<RecordRow[]>("/admin/" + view + "?q=" + encodeURIComponent(query)).then((value) => { if (active) setRows(value); })
      .catch((failure) => { if (active) report(failure); });
    return () => { active = false; };
  }, [view, query, report]);

  useEffect(() => {
    if (view === "broadcasts" || view === "policy-rules") api<RecordRow[]>("/admin/segments").then(setSegments).catch(report);
  }, [view, report]);

  useEffect(() => {
    let active = true;
    setMembers([]);
    if (segment) api<RecordRow[]>(`/admin/segments/${segment}/members`)
      .then((value) => { if (active) setMembers(value); }).catch((failure) => { if (active) report(failure); });
    return () => { active = false; };
  }, [segment, report]);

  async function update(task: () => Promise<unknown>) {
    setError(""); setBusy(true);
    try {
      await task();
      const next = await api<RecordRow[]>("/admin/" + view + "?q=" + encodeURIComponent(query));
      setRows(next);
      if (segment) setMembers(await api(`/admin/segments/${segment}/members`));
    } catch (failure) { report(failure); }
    finally { setBusy(false); }
  }

  function submit(event: FormEvent<HTMLFormElement>, build: (form:FormData) => Promise<unknown>) {
    event.preventDefault();
    const form = event.currentTarget;
    const values = new FormData(form);
    void update(async () => { await build(values); form.reset(); });
  }

  function controls(row:RecordRow) {
    const id = String(row.id);
    if (view === "users" && !row.disabled && !row.admin) return <button disabled={busy} onClick={() => {
      if (confirm("Khóa tài khoản " + row.username + "?")) void update(() => api(`/admin/users/${id}/disable`,"POST"));
    }}>Khóa</button>;
    if (view === "segments") return <button onClick={() => {setSegment(id);setPeople([]);}}>Thành viên</button>;
    if (view === "requests" && row.status === "pending" && row.requester_id !== user.id) return <div className="actions">
      <button disabled={busy} onClick={() => update(() => api(`/admin/requests/${id}/approve`,"POST"))}>Duyệt</button>
      <button disabled={busy} onClick={() => update(() => api(`/admin/requests/${id}/reject`,"POST"))}>Từ chối</button>
    </div>;
    if (view === "broadcasts" && row.status === "scheduled") return <button disabled={busy}
      onClick={() => update(() => api(`/admin/broadcasts/${id}/cancel`,"POST"))}>Hủy lịch</button>;
    if (view === "policy-rules") return <div className="actions">
      <button disabled={busy} onClick={() => update(() => api(`/admin/policy-rules/${id}`,"PUT",{
        action:row.action,effect:row.effect,enabled:!row.enabled,
        subject_segment:row.subject_segment ?? "",object_segment:row.object_segment ?? "",
      }))}>{row.enabled ? "Tắt" : "Bật"}</button>
      <button disabled={busy} onClick={() => {
        if (confirm("Xóa luật phân quyền này?")) void update(() => api(`/admin/policy-rules/${id}`,"DELETE"));
      }}>Xóa</button>
    </div>;
    return null;
  }

  return <div className="layout"><nav aria-label="Quản trị">
    {Object.entries(labels).map(([id,label]) => <button key={id} disabled={busy} aria-current={view === id}
      onClick={() => {setView(id as View);setRows([]);setSegment("");setError("");setQuery("");}}>{label}</button>)}
  </nav><main className="content">
    <h1>{labels[view]}</h1>
    {error && <p role="alert">{error}</p>}
    {view === "users" && <form onSubmit={(event) => {
      event.preventDefault(); setQuery(String(new FormData(event.currentTarget).get("q")));
    }}><label>Tìm tài khoản<input name="q"/></label><button>Tìm</button></form>}
    {view === "segments" && <form onSubmit={(event) => submit(event,(values) => api("/admin/segments","POST",{
      key:values.get("key"),name:values.get("name"),sensitive:values.has("sensitive"),
    }))}>
      <label>Mã segment<input name="key" required maxLength={100}/></label>
      <label>Tên<input name="name" required maxLength={150}/></label>
      <label><span><input name="sensitive" type="checkbox"/> Nhạy cảm</span></label>
      <button disabled={busy}>Tạo segment</button>
    </form>}
    {view === "broadcasts" && <form onSubmit={(event) => submit(event,(values) => {
      const schedule = String(values.get("scheduled"));
      return api("/admin/broadcasts","POST",{
        segment_id:values.get("segment"),body:values.get("body"),scheduled_at:schedule ? new Date(schedule).toISOString() : null,
      });
    })}>
      <label>Segment<select name="segment" required>
        <option value="">Chọn segment</option>
        {segments.filter((row) => !row.sensitive).map((row) => <option key={String(row.id)} value={String(row.id)}>{row.name}</option>)}
      </select></label>
      <label>Nội dung<textarea name="body" required maxLength={16000}/></label>
      <label>Lịch gửi<input type="datetime-local" name="scheduled"/></label>
      <button disabled={busy}>Gửi / Lên lịch</button>
    </form>}
    {view === "retention" && <form onSubmit={(event) => submit(event,(values) => api("/admin/retention","PUT",{
      scope:values.get("scope"),keep_days:Number(values.get("days")),
    }))}>
      <label>Phạm vi<select name="scope">
        <option value="global">Toàn hệ thống</option><option value="private">Kênh riêng</option>
        <option value="dm">DM</option><option value="group_dm">DM nhóm</option><option value="announcement">Thông báo</option>
      </select></label>
      <label>Số ngày lưu<input name="days" type="number" min={1} max={3650} required /></label>
      <button disabled={busy}>Lưu chính sách</button>
    </form>}
    {view === "policy-rules" && <form onSubmit={(event) => submit(event,(values) => api("/admin/policy-rules","POST",{
      subject_segment:values.get("subject"),object_segment:values.get("object"),
      action:values.get("action"),effect:values.get("effect"),enabled:true,
    }))}>
      {["subject","object"].map((name) => <label key={name}>{name==="subject" ? "Segment nguồn" : "Segment đích"}<select name={name}>
        <option value="">Tất cả</option>
        {segments.map((row) => <option key={String(row.id)} value={String(row.id)}>{row.name}</option>)}
      </select></label>)}
      <label>Hành động<select name="action">
        {["dm.create","group_dm.create","channel.create","message.send","broadcast.send"].map((action) => <option key={action}>{action}</option>)}
      </select></label>
      <label>Hiệu lực<select name="effect"><option value="deny">Từ chối</option><option value="allow">Cho phép</option></select></label>
      <button disabled={busy}>Tạo luật</button>
    </form>}
    <Table rows={rows} fields={columns[view]} action={["users","segments","requests","broadcasts","policy-rules"].includes(view) ? controls : undefined}/>
    {view === "segments" && segment && <section>
      <h2>Thành viên · {rows.find((row) => row.id === segment)?.name}</h2>
      <form onSubmit={(event) => {
        event.preventDefault();
        const q=String(new FormData(event.currentTarget).get("q"));
        api<RecordRow[]>("/admin/users?q="+encodeURIComponent(q)).then(setPeople).catch(report);
      }}><label>Tìm người để thêm<input name="q" required/></label><button>Tìm</button></form>
      {people.length > 0 && <form onSubmit={(event) => submit(event,(values) => api(`/admin/segments/${segment}/members`,"POST",{
        user_id:values.get("user"), role:values.get("role"),
      }))}>
        <label>Tài khoản<select required name="user">{people.map((row) => <option key={String(row.id)} value={String(row.id)}>{row.username}</option>)}</select></label>
        <label>Vai trò<select name="role"><option value="member">Thành viên</option><option value="manager">Quản lý</option><option value="owner">Chủ sở hữu</option></select></label>
        <button disabled={busy}>Lưu / Yêu cầu duyệt</button>
      </form>}
      <Table rows={members} fields={[["username","Tài khoản"],["display_name","Tên"],["role","Vai trò"]]} action={(row) =>
        <button disabled={busy} onClick={() => {
          if (confirm("Gỡ thành viên " + row.username + "?")) void update(() => api(`/admin/segments/${segment}/members`,"POST",{user_id:row.user_id, role:"remove"}));
        }}>Gỡ</button>
      }/>
    </section>}
  </main></div>;
}

createRoot(document.getElementById("root")!).render(<Session admin>{(user) => <Admin key={user.id} user={user}/>}</Session>);
