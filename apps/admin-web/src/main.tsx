import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { api, ApiError, type User } from "../../../packages/api-client/session";
import { Session } from "../../../packages/api-client/session-view";
import "../../../packages/api-client/styles.css";

type RecordRow = Record<string, string | boolean | number | null>;
type View = "users" | "segments" | "requests" | "broadcasts" | "reports" | "audit" | "retention" | "policy-rules";
const labels: Record<View, string> = {
  users:"Directory", segments:"Segment", requests:"Approvals",
  broadcasts:"Announcements", reports:"Reports", audit:"Audit log", retention:"Data retention", "policy-rules":"Policy rules",
};
const columns: Record<View, [string,string][]> = {
  users:[["username","Username"],["display_name","Name"],["disabled","Manually disabled"],["ad_disabled","Disabled in AD"],["admin","Administration"]],
  segments:[["key","Key"],["name","Name"],["sensitive","Sensitive"]],
  requests:[["segment_name","Segment"],["username","Username"],["role","Change"],["status","Status"]],
  broadcasts:[["segment_name","Segment"],["body","Message"],["scheduled_at","Scheduled for"],["status","Status"],["recipients","Recipients"],["readers","Read"]],
  reports:[["message_id","Message"],["reason","Reason"],["created_at","Time"]],
  audit:[["action","Action"],["actor_id","Actor"],["target","Target"],["created_at","Time"]],
  retention:[["scope","Scope"],["keep_days","Retention days"],["updated_at","Updated"]],
  "policy-rules":[["action","Action"],["effect","Effect"],["subject","Subject segment"],["object","Target segment"],["enabled","Enabled"]],
};

function Table({rows, fields, action}: {rows:RecordRow[]; fields:[string,string][]; action?:(row:RecordRow)=>ReactNode}) {
  if (rows.length === 0) return <p>No data yet.</p>;
  return <div className="table"><table>
    <thead><tr>{fields.map(([key,title]) => <th key={key} scope="col">{title}</th>)}{action && <th scope="col">Actions</th>}</tr></thead>
    <tbody>{rows.map((row) => <tr key={String(row.id ?? row.user_id)}>
      {fields.map(([key]) => <td key={key}>{typeof row[key] === "boolean" ? (row[key] ? "Yes" : "No") : String(row[key] ?? "—")}</td>)}
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
    setError(failure instanceof ApiError ? "Action failed (" + failure.code + ")." : "Could not connect to the server.");
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
      if (confirm("Disable account " + row.username + "?")) void update(() => api(`/admin/users/${id}/disable`,"POST"));
    }}>Disable</button>;
    if (view === "segments") return <button onClick={() => {setSegment(id);setPeople([]);}}>Members</button>;
    if (view === "requests" && row.status === "pending" && row.requester_id !== user.id) return <div className="actions">
      <button disabled={busy} onClick={() => update(() => api(`/admin/requests/${id}/approve`,"POST"))}>Approve</button>
      <button disabled={busy} onClick={() => update(() => api(`/admin/requests/${id}/reject`,"POST"))}>Reject</button>
    </div>;
    if (view === "broadcasts" && row.status === "scheduled") return <button disabled={busy}
      onClick={() => update(() => api(`/admin/broadcasts/${id}/cancel`,"POST"))}>Cancel schedule</button>;
    if (view === "policy-rules") return <div className="actions">
      <button disabled={busy} onClick={() => update(() => api(`/admin/policy-rules/${id}`,"PUT",{
        action:row.action,effect:row.effect,enabled:!row.enabled,
        subject_segment:row.subject_segment ?? "",object_segment:row.object_segment ?? "",
      }))}>{row.enabled ? "Disable" : "Enable"}</button>
      <button disabled={busy} onClick={() => {
        if (confirm("Delete this policy rule?")) void update(() => api(`/admin/policy-rules/${id}`,"DELETE"));
      }}>Delete</button>
    </div>;
    return null;
  }

  return <div className="layout"><nav aria-label="Administration">
    {Object.entries(labels).map(([id,label]) => <button key={id} disabled={busy} aria-current={view === id}
      onClick={() => {setView(id as View);setRows([]);setSegment("");setError("");setQuery("");}}>{label}</button>)}
  </nav><main className="content">
    <h1>{labels[view]}</h1>
    {error && <p role="alert">{error}</p>}
    {view === "users" && <form onSubmit={(event) => {
      event.preventDefault(); setQuery(String(new FormData(event.currentTarget).get("q")));
    }}><label>Find accounts<input name="q"/></label><button>Search</button></form>}
    {view === "segments" && <form onSubmit={(event) => submit(event,(values) => api("/admin/segments","POST",{
      key:values.get("key"),name:values.get("name"),sensitive:values.has("sensitive"),
    }))}>
      <label>Segment key<input name="key" required maxLength={100}/></label>
      <label>Name<input name="name" required maxLength={150}/></label>
      <label><span><input name="sensitive" type="checkbox"/> Sensitive</span></label>
      <button disabled={busy}>Create segment</button>
    </form>}
    {view === "broadcasts" && <form onSubmit={(event) => submit(event,(values) => {
      const schedule = String(values.get("scheduled"));
      return api("/admin/broadcasts","POST",{
        segment_id:values.get("segment"),body:values.get("body"),scheduled_at:schedule ? new Date(schedule).toISOString() : null,
      });
    })}>
      <label>Segment<select name="segment" required>
        <option value="">Select a segment</option>
        {segments.filter((row) => !row.sensitive).map((row) => <option key={String(row.id)} value={String(row.id)}>{row.name}</option>)}
      </select></label>
      <label>Message<textarea name="body" required maxLength={16000}/></label>
      <label>Scheduled for<input type="datetime-local" name="scheduled"/></label>
      <button disabled={busy}>Send or schedule</button>
    </form>}
    {view === "retention" && <form onSubmit={(event) => submit(event,(values) => api("/admin/retention","PUT",{
      scope:values.get("scope"),keep_days:Number(values.get("days")),
    }))}>
      <label>Scope<select name="scope">
        <option value="global">System-wide</option><option value="private">Private channel</option>
        <option value="dm">DM</option><option value="group_dm">Group DM</option><option value="announcement">Announcements</option>
      </select></label>
      <label>Retention days<input name="days" type="number" min={1} max={3650} required /></label>
      <button disabled={busy}>Save policy</button>
    </form>}
    {view === "policy-rules" && <form onSubmit={(event) => submit(event,(values) => api("/admin/policy-rules","POST",{
      subject_segment:values.get("subject"),object_segment:values.get("object"),
      action:values.get("action"),effect:values.get("effect"),enabled:true,
    }))}>
      {["subject","object"].map((name) => <label key={name}>{name==="subject" ? "Subject segment" : "Target segment"}<select name={name}>
        <option value="">All</option>
        {segments.map((row) => <option key={String(row.id)} value={String(row.id)}>{row.name}</option>)}
      </select></label>)}
      <label>Action<select name="action">
        {["dm.create","group_dm.create","channel.create","message.send","broadcast.send"].map((action) => <option key={action}>{action}</option>)}
      </select></label>
      <label>Effect<select name="effect"><option value="deny">Deny</option><option value="allow">Allow</option></select></label>
      <button disabled={busy}>Create rule</button>
    </form>}
    <Table rows={rows} fields={columns[view]} action={["users","segments","requests","broadcasts","policy-rules"].includes(view) ? controls : undefined}/>
    {view === "segments" && segment && <section>
      <h2>Members · {rows.find((row) => row.id === segment)?.name}</h2>
      <form onSubmit={(event) => {
        event.preventDefault();
        const q=String(new FormData(event.currentTarget).get("q"));
        api<RecordRow[]>("/admin/users?q="+encodeURIComponent(q)).then(setPeople).catch(report);
      }}><label>Find people to add<input name="q" required/></label><button>Search</button></form>
      {people.length > 0 && <form onSubmit={(event) => submit(event,(values) => api(`/admin/segments/${segment}/members`,"POST",{
        user_id:values.get("user"), role:values.get("role"),
      }))}>
        <label>Username<select required name="user">{people.map((row) => <option key={String(row.id)} value={String(row.id)}>{row.username}</option>)}</select></label>
        <label>Role<select name="role"><option value="member">Member</option><option value="manager">Manager</option><option value="owner">Owner</option></select></label>
        <button disabled={busy}>Save or request approval</button>
      </form>}
      <Table rows={members} fields={[["username","Username"],["display_name","Name"],["role","Role"]]} action={(row) =>
        <button disabled={busy} onClick={() => {
          if (confirm("Remove member " + row.username + "?")) void update(() => api(`/admin/segments/${segment}/members`,"POST",{user_id:row.user_id, role:"remove"}));
        }}>Remove</button>
      }/>
    </section>}
  </main></div>;
}

createRoot(document.getElementById("root")!).render(<Session admin>{(user) => <Admin key={user.id} user={user}/>}</Session>);
