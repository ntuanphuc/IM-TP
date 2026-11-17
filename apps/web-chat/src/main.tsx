import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { createRoot } from "react-dom/client";
import { api, ApiError, download, refresh, socketToken, type User } from "../../../packages/api-client/session";
import { Session } from "../../../packages/api-client/session-view";
import { ImagePreview } from "./image-preview";
import "../../../packages/api-client/styles.css";
import "./chat.css";

interface Channel { id: string; name: string; unread: number; last_read_seq: number; kind: string; preview: string | null; preview_at: string | null }
interface Message {
  id: string; channel_id: string; seq: number; sender_id: string; sender_name: string;
  parent_id: string | null; body: string; created_at: string;
  reactions: {emoji: string; user_id: string}[];
  files: {id:string; name:string; status:string; mime:string}[];
}
interface Person { id: string; display_name: string; username: string }
interface Announcement { id: string; body: string; scheduled_at: string; read_at: string | null }
interface Pending { channel: string; body: string; parent_id: string; client_msg_id: string; attachment_ids:string[] }
const initialChannel = () => new URLSearchParams(location.search).get("channel") ?? "";
const timestamp = (value: string) => new Date(value).toLocaleString("vi-VN");
const shortTime = (value: string) => new Date(value).toLocaleTimeString("vi-VN", {hour:"2-digit", minute:"2-digit"});
const channelType: Record<string,string> = {dm:"Tin nhắn riêng", group_dm:"Nhóm chat", private:"Kênh riêng", announcement:"Thông báo"};
type PeopleMode = "direct" | "group" | "channel";

function Avatar({name, kind = "dm"}: {name:string; kind?:string}) {
  const initials = name.trim().split(/\s+/).filter(Boolean).slice(-2).map((part) => Array.from(part)[0]).join("");
  return <span className={`chat-avatar avatar-${kind}`} aria-hidden="true">{kind === "private" ? "#" : initials || "?"}</span>;
}
const mergeMessages = (previous: Message[], latest: Message[]) =>
  [...new Map([...previous, ...latest].map((message) => [message.id, message])).values()].sort((a,b) => a.seq-b.seq);

function Chat({user}: {user: User}) {
  const [channels, setChannels] = useState<Channel[]>([]);
  const [selected, setSelected] = useState(initialChannel);
  const [messages, setMessages] = useState<Message[]>([]);
  const [thread, setThread] = useState("");
  const [replies, setReplies] = useState<Message[]>([]);
  const [people, setPeople] = useState<Person[]>([]);
  const [peopleMode, setPeopleMode] = useState<PeopleMode>("direct");
  const [recipients, setRecipients] = useState<Person[]>([]);
  const [peopleLoading, setPeopleLoading] = useState(false);
  const [creating, setCreating] = useState(false);
  const peopleRequest = useRef(0);
  const [results, setResults] = useState<Message[]>([]);
  const [announcements, setAnnouncements] = useState<Announcement[]>([]);
  const [panel, setPanel] = useState<"chat" | "people" | "search" | "broadcast">("chat");
  const [draft, setDraft] = useState("");
  const [attachment, setAttachment] = useState<File | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const [pending, setPending] = useState<Pending | null>(null);
  const [error, setError] = useState("");
  const [status, setStatus] = useState("Đang kết nối");
  const [busy, setBusy] = useState(false);
  const [infoOpen, setInfoOpen] = useState(false);
  const [theme, setTheme] = useState(() => localStorage.getItem("chattp-theme") === "dark" ? "dark" : "light");
  const timeline = useRef<HTMLDivElement>(null);
  const scrollState = useRef({channel:"", lastID:"", follow:true});
  useEffect(() => {
    const box = timeline.current;
    if (!box) return;
    const lastID = messages.at(-1)?.id ?? "";
    const state = scrollState.current;
    if (state.channel !== selected || (lastID !== state.lastID && state.follow)) box.scrollTop = box.scrollHeight;
    state.channel = selected;
    state.lastID = lastID;
  }, [selected, messages, panel]);
  const active = useRef({selected, thread});
  active.current = {selected, thread};

  const report = useCallback((failure: unknown) => {
    if (failure instanceof ApiError && failure.status === 403) {
      setError(failure.code === "teacher_required"
        ? "Nhóm có học sinh cần ít nhất một giáo viên."
        : "Tài khoản của bạn chưa được phép thực hiện thao tác này.");
      return;
    }
    setError(failure instanceof ApiError ? "Yêu cầu không thành công (" + failure.code + ")." : "Mất kết nối. Hãy thử lại.");
  }, []);

  const load = useCallback(async () => {
    const {selected: channel, thread: parent} = active.current;
    const list = await api<Channel[]>("/channels");
    setChannels(list);
    const visible = new Set(list.map((item) => item.id));
    setResults((previous) => previous.filter((message) => visible.has(message.channel_id)));
    if (!channel) return;
    if (!list.some((item) => item.id === channel)) {
      if (active.current.selected === channel) {
        setMessages([]); setReplies([]); setThread(""); setPending(null);
        setError("Bạn không có quyền truy cập cuộc trò chuyện này.");
      }
      return;
    }
    try {
      const rows = await api<Message[]>(`/channels/${channel}/messages`);
      if (active.current.selected === channel) setMessages(rows);
      if (parent) {
        const next = await api<Message[]>(`/messages/${parent}/thread`);
        if (active.current.thread === parent) setReplies(next);
      }
    } catch (failure) {
      if (failure instanceof ApiError && [403,404].includes(failure.status) && active.current.selected === channel) {
        setMessages([]); setReplies([]); setThread(""); setPending(null);
      }
      throw failure;
    }
  }, []);

  useEffect(() => { load().catch(report); }, [selected, thread, load, report]);
  const channel = channels.find((item) => item.id === selected);
  const submittedReads = useRef(new Map<string, number>());
  useEffect(() => {
    const box = timeline.current;
    if (!box || panel !== "chat" || !channel?.unread) return;
    const {id, last_read_seq} = channel;
    const incoming = box.querySelectorAll<HTMLElement>("[data-read-seq]");
    let pendingTimer: ReturnType<typeof setTimeout>;
    function schedule() {
      clearTimeout(pendingTimer);
      pendingTimer = setTimeout(() => {
        if (!box || !box.clientHeight || document.visibilityState !== "visible" || !document.hasFocus()) return;
        const viewport = box.getBoundingClientRect();
        let seen = Math.max(last_read_seq, submittedReads.current.get(id) ?? 0);
        const previous = seen;
        for (const row of incoming) {
          const seq = Number(row.dataset.readSeq);
          if (seq <= seen) continue;
          const bounds = row.getBoundingClientRect();
          const visible = Math.min(viewport.bottom, bounds.bottom) - Math.max(viewport.top, bounds.top);
          if (visible >= Math.min(bounds.height / 2, 120) && visible > 0) seen = seq;
        }
        if (seen === previous) return;
        submittedReads.current.set(id, seen);
        api(`/channels/${id}/read`, "POST", {seq:seen}).then(load).catch(() => {
          if (submittedReads.current.get(id) === seen) submittedReads.current.set(id, previous);
        });
      }, 250);
    }
    const resize = new ResizeObserver(schedule);
    resize.observe(box);
    for (const row of incoming) resize.observe(row);
    box.addEventListener("scroll", schedule, {passive:true});
    document.addEventListener("visibilitychange", schedule);
    window.addEventListener("focus", schedule);
    schedule();
    return () => {
      clearTimeout(pendingTimer);
      resize.disconnect();
      box.removeEventListener("scroll", schedule);
      document.removeEventListener("visibilitychange", schedule);
      window.removeEventListener("focus", schedule);
    };
  }, [channel, infoOpen, load, messages, panel, replies]);
  useEffect(() => {
    const navigate = () => { setSelected(initialChannel()); setMessages([]); setThread(""); setDraft(""); setAttachment(null); setPanel("chat"); };
    window.addEventListener("popstate", navigate);
    return () => window.removeEventListener("popstate", navigate);
  }, []);

  useEffect(() => {
    let closed = false, cursor: number | undefined, attempt = 0, refreshing = false, invalidated = false;
    let socket: WebSocket | undefined;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let batch: ReturnType<typeof setTimeout> | undefined;

    function invalidate() {
      invalidated = true;
      if (batch) return;
      batch = setTimeout(async () => {
        invalidated = false;
        try { await load(); } catch (error) { if (!closed) report(error); }
        finally { batch = undefined; if (!closed && invalidated) invalidate(); }
      }, 120);
    }

    async function reconnect(code: number) {
      if (closed) return;
      setStatus("Đang kết nối lại");
      if (code === 1008 && !refreshing) {
        refreshing = true;
        try { await api<User>("/me"); }
        catch { if (!closed) setStatus("Hết phiên hoặc mất kết nối"); }
        finally { refreshing = false; }
      }
      if (!closed) timer = setTimeout(connect, Math.min(15000, 500 * 2 ** attempt++));
    }

    function connect() {
      const scheme = location.protocol === "https:" ? "wss:" : "ws:";
      const current = new WebSocket(scheme + "//" + location.host + "/v1/ws");
      socket = current;
      current.onopen = () => current.send(JSON.stringify({type:"auth",data:{token:socketToken()}}));
      current.onmessage = ({data}) => {
        if (closed) return;
        let frame;
        try { frame = JSON.parse(data); } catch { return; }
        if (frame.type === "auth.ok") {
          if (typeof frame.data?.cursor !== "number") return;
          if (cursor !== undefined && cursor <= frame.data.cursor) {
            current.send(JSON.stringify({type:"resume",data:{cursor}}));
          } else cursor = frame.data.cursor;
          attempt = 0;
          setStatus("Đã kết nối");
          invalidate();
        } else if (frame.type === "auth.expiring") {
          refresh().then(() => {
            if (current.readyState === WebSocket.OPEN) current.send(JSON.stringify({type:"auth",data:{token:socketToken()}}));
          }).catch(() => current.close());
        } else if (typeof frame.id === "number" && (cursor === undefined || frame.id > cursor)) {
          cursor = frame.id;
          invalidate();
          if (frame.type === "message.deleted") {
            setResults((previous) => previous.filter((message) => message.id !== frame.data?.id));
          }
          if (frame.type === "broadcast.new" || frame.type === "message.deleted") {
            api<Announcement[]>("/broadcasts").then(setAnnouncements).catch(report);
          }
        }
      };
      current.onclose = ({code}) => { void reconnect(code); };
    }
    connect();
    const heartbeat = setInterval(() => {
      if (socket?.readyState === WebSocket.OPEN) socket.send(JSON.stringify({type:"ping",data:{}}));
    }, 25000);
    return () => { closed = true; clearTimeout(timer); clearTimeout(batch); clearInterval(heartbeat); socket?.close(); };
  }, [load, report]);

  function open(id: string) {
    setInfoOpen(false);
    if (id !== selected) {
      setMessages([]);
      setThread("");
      setDraft("");
      setAttachment(null);
      if (fileInput.current) fileInput.current.value = "";
      scrollState.current.follow = true;
    }
    setSelected(id);
    setPanel("chat");
    setError("");
    const url = new URL(location.href);
    url.searchParams.set("channel",id);
    history.pushState({}, "", url);
  }

  async function action(task: () => Promise<unknown>) {
    setError("");
    try { await task(); } catch (failure) { report(failure); }
  }

  async function findPeople(query: string) {
    const request = ++peopleRequest.current;
    setPeopleLoading(true);
    try {
      const found = await api<Person[]>("/users?q=" + encodeURIComponent(query.trim()));
      if (request === peopleRequest.current) setPeople(found);
    } catch (failure) {
      if (request === peopleRequest.current) report(failure);
    } finally {
      if (request === peopleRequest.current) setPeopleLoading(false);
    }
  }

  function startConversation() {
    setPanel("people");
    setPeopleMode("direct");
    setRecipients([]);
    setPeople([]);
    setError("");
    void findPeople("");
  }

  async function createConversation(mode: PeopleMode, users: Person[], name = "") {
    if (creating || (mode === "group" && users.length < 2) || (mode === "channel" && !name.trim())) return;
    setCreating(true);
    await action(async () => {
      const result = await api<{id:string}>(mode === "channel" ? "/channels" : "/dms","POST",{
        name:name.trim(),user_ids:users.map((person) => person.id),
      });
      open(result.id);
    });
    setCreating(false);
  }

  async function send(item: Pending) {
    setBusy(true);
    setPending(item);
    try {
      await api(`/channels/${item.channel}/messages`, "POST", {
        body:item.body, parent_id:item.parent_id, client_msg_id:item.client_msg_id,attachment_ids:item.attachment_ids,
      });
      setPending(null);
      setDraft("");
      setAttachment(null);
      if (fileInput.current) fileInput.current.value="";
      await load();
    } catch (failure) {
      if (failure instanceof ApiError && failure.status < 500 && failure.status !== 429) setPending(null);
      report(failure);
    } finally { setBusy(false); }
  }

  async function sendForm(event: FormEvent) {
    event.preventDefault();
    if (busy || pending || !selected || (!draft.trim() && !attachment)) return;
    const attachment_ids:string[]=[];
    if (attachment) {
      setBusy(true);
      try {
        const body=new FormData();
        body.set("file",attachment);
        const file=await api<{id:string}>(`/channels/${selected}/files`,"POST",body);
        attachment_ids.push(file.id);
      } catch (failure) { report(failure); setBusy(false); return; }
    }
    await send({channel:selected,body:draft,parent_id:thread,client_msg_id:crypto.randomUUID(),attachment_ids});
  }

  function messageCard(message: Message, index: number, list: Message[]) {
    const reacted = message.reactions.some((reaction) => reaction.user_id === user.id && reaction.emoji === "👍");
    const own = message.sender_id === user.id;
    const groupedWith = (other: Message | undefined) => other?.sender_id === message.sender_id
      && Math.abs(Date.parse(message.created_at) - Date.parse(other.created_at)) < 300_000;
    const continuation = groupedWith(list[index-1]);
    const last = !groupedWith(list[index+1]);
    return <article data-read-seq={!own ? message.seq : undefined} className={`bubble-row ${own ? "bubble-own" : "bubble-peer"} ${last ? "bubble-last" : "bubble-grouped"}`} key={message.id}>
      {!own && (last ? <Avatar name={message.sender_name}/> : <span className="avatar-space" />)}
      <div className="bubble-content">
      {!own && !continuation && <div className="bubble-meta"><strong>{message.sender_name}</strong></div>}
      <div className="chat-bubble">
      {message.files.map((file) => file.status === "clean" && ["image/jpeg","image/png","image/gif"].includes(file.mime)
        ? <ImagePreview key={file.id} id={file.id} name={file.name} onDownload={() => {void action(() => download("/files/"+file.id,file.name));}} />
        : <p key={file.id}>{file.name} {file.status === "clean"
        ? <button onClick={() => action(() => download("/files/"+file.id,file.name))}>Tải xuống</button>
        : file.status === "pending" ? "· Đang quét" : "· Bị chặn"}</p>)}
      <div className="bubble-line">{message.body && <p>{message.body}</p>}
        <span className="bubble-clock"><time dateTime={message.created_at} title={timestamp(message.created_at)}>{shortTime(message.created_at)}</time>
          {own && <span title="Đã gửi" aria-label="Đã gửi">✓</span>}</span>
      </div>
      </div>
      <div className="actions bubble-actions">
        {!message.parent_id && <button onClick={() => setThread(message.id)}>Thảo luận</button>}
        <button aria-pressed={reacted} onClick={() => action(async () => {
          await api(`/messages/${message.id}/reactions/${encodeURIComponent("👍")}`,reacted ? "DELETE" : "PUT"); await load();
        })}>👍 {message.reactions.filter((reaction) => reaction.emoji === "👍").length}</button>
        {message.sender_id === user.id && <>
          <button onClick={() => {
            const body = prompt("Sửa tin nhắn", message.body);
            if (body?.trim()) void action(async () => { await api(`/messages/${message.id}`,"PATCH",{body}); await load(); });
          }}>Sửa</button>
          <button onClick={() => {
            if (confirm("Xóa tin nhắn này?")) void action(async () => { await api(`/messages/${message.id}`,"DELETE"); await load(); });
          }}>Xóa</button>
        </>}
        <button onClick={() => {
          const reason = prompt("Lý do báo cáo");
          if (reason?.trim()) void action(() => api(`/messages/${message.id}/report`,"POST",{reason}));
        }}>Báo cáo</button>
      </div>
      </div>
    </article>;
  }

  const conversations = channels.filter((item) => item.kind !== "announcement");
  const recentFiles = messages.flatMap((message) => message.files).filter((file) => file.status === "clean").slice(-8).reverse();
  function showInbox() {
    setSelected("");
    setPanel("chat");
    setThread("");
    setInfoOpen(false);
    setMessages([]);
    setDraft("");
    setAttachment(null);
    const url = new URL(location.href);
    url.searchParams.delete("channel");
    history.pushState({}, "", url);
  }

  return <div data-theme={theme} className={`chat-workspace ${panel !== "chat" || selected ? "chat-open" : ""} ${infoOpen && panel === "chat" && channel ? "with-info" : ""}`}>
    <nav className="chat-sidebar" aria-label="Hội thoại">
      <div className="inbox-heading"><div><h1>Đoạn chat</h1>
        <span className="connection" role="status" data-connected={status === "Đã kết nối"}>{status}</span></div>
        <button className="chat-primary" onClick={startConversation}>＋ Tin mới</button>
      </div>
      <div className="inbox-tools">
      <button aria-pressed={theme === "dark"} onClick={() => {
        const next = theme === "dark" ? "light" : "dark";
        setTheme(next);
        localStorage.setItem("chattp-theme",next);
      }}>{theme === "dark" ? "Giao diện sáng" : "Giao diện tối"}</button>
      <button aria-pressed={panel === "search"} onClick={() => {setPanel("search");setError("");}}>Tìm tin nhắn</button>
      <button aria-pressed={panel === "broadcast"} onClick={() => { setPanel("broadcast"); void action(async () => setAnnouncements(await api("/broadcasts"))); }}>Thông báo</button>
      </div>
      <div className="conversation-list">
      {conversations.length === 0 && <p className="chat-muted inbox-empty">Chưa có cuộc trò chuyện.</p>}
      {conversations.map((item) =>
        <button className="conversation" key={item.id} aria-current={panel === "chat" && item.id === selected} onClick={() => open(item.id)}>
          <Avatar name={item.name} kind={item.kind}/>
          <span className="conversation-text"><strong>{item.name}</strong><span>{item.preview || channelType[item.kind]}</span></span>
          <span className="conversation-meta">{item.preview_at && <time dateTime={item.preview_at}>{shortTime(item.preview_at)}</time>}
          {item.unread > 0 && <span className="unread" aria-label={`${item.unread} tin chưa đọc`}>{item.unread}</span>}</span>
        </button>)}
      </div>
    </nav>
    <main className={`chat-main ${panel === "chat" && channel ? "active-chat" : ""}`}>
      {(panel !== "chat" || selected !== "") && <button className="back-inbox" onClick={showInbox}>‹ Đoạn chat</button>}
      {error && <p className="chat-error" role="alert">{error}</p>}
      {panel === "people" && <section className="chat-panel">
        <h1>Tin nhắn mới</h1>
        <div className="compose-modes" aria-label="Loại hội thoại">
          {(["direct","group","channel"] as const).map((mode) => <button key={mode} disabled={creating} aria-pressed={peopleMode === mode}
            onClick={() => {setPeopleMode(mode);setRecipients([]);setError("");}}>
            {{direct:"Nhắn riêng",group:"Tạo nhóm chat",channel:"Tạo kênh riêng"}[mode]}
          </button>)}
        </div>
        <p className="chat-muted">{{
          direct:"Tìm một người để bắt đầu hoặc mở lại đoạn chat 1:1.",
          group:"Chọn ít nhất hai người để trò chuyện cùng bạn.",
          channel:"Đặt tên cho một kênh theo lớp, dự án hoặc phòng ban rồi chọn người tham gia.",
        }[peopleMode]}</p>
        <form onSubmit={(event) => {
          event.preventDefault(); const values = new FormData(event.currentTarget);
          void findPeople(String(values.get("q")));
        }}><label>Tìm người<input type="search" name="q" placeholder="Tên hoặc tài khoản"/></label><button>Tìm</button></form>
        {peopleMode !== "direct" && recipients.length > 0 && <div className="selected-people">{recipients.map((person) =>
          <button key={person.id} type="button" onClick={() => setRecipients((current) => current.filter((item) => item.id !== person.id))}
            aria-label={`Bỏ chọn ${person.display_name}`}>{person.display_name} ×</button>)}</div>}
        <div className="people-list" aria-busy={peopleLoading}>
          {peopleLoading ? <p className="chat-muted">Đang tìm…</p> : people.length === 0 ? <p className="chat-muted">Không tìm thấy người dùng.</p> :
            people.map((person) => <div className="person-row" key={person.id}>
              <Avatar name={person.display_name}/><span className="person-name"><strong>{person.display_name}</strong><small>{person.username}</small></span>
              {peopleMode === "direct" ? <button className="chat-primary" disabled={creating} onClick={() => void createConversation("direct",[person])}>Nhắn tin</button> :
                <input type="checkbox" aria-label={`Chọn ${person.display_name}`} checked={recipients.some((item) => item.id === person.id)}
                 onChange={(event) => {
                  const checked = event.target.checked;
                  setRecipients((current) => checked ? [...current,person] : current.filter((item) => item.id !== person.id));
                 }}/>}
            </div>)}
        </div>
        {peopleMode !== "direct" && <form className="create-conversation" key={peopleMode} onSubmit={(event) => {
          event.preventDefault();
          const name = String(new FormData(event.currentTarget).get("name") ?? "");
          void createConversation(peopleMode,recipients,name);
        }}>
          <label>{peopleMode === "channel" ? "Tên kênh" : "Tên nhóm (tùy chọn)"}<input name="name" maxLength={120} required={peopleMode === "channel"}/></label>
          <button className="chat-primary" disabled={creating || (peopleMode === "group" && recipients.length < 2)}>
            {peopleMode === "channel" ? "Tạo kênh" : "Tạo nhóm"}
          </button>
        </form>}
      </section>}
      {panel === "search" && <section className="chat-panel">
        <h1>Tìm tin nhắn</h1>
        <form onSubmit={(event) => {
          event.preventDefault(); const values = new FormData(event.currentTarget);
          void action(async () => setResults(await api("/search?q="+encodeURIComponent(String(values.get("q"))))));
        }}><label>Từ khóa<input name="q" required/></label><button>Tìm</button></form>
        {results.map((message) => <article className="message" key={message.id}>
          <p>{message.body}</p><button onClick={() => open(message.channel_id)}>Mở hội thoại</button>
        </article>)}
      </section>}
      {panel === "broadcast" && <section className="chat-panel">
        <h1>Thông báo</h1>
        {announcements.length === 0 && <p>Chưa có thông báo.</p>}
        {announcements.map((item) => <article className="message" key={item.id}>
          <time dateTime={item.scheduled_at}>{timestamp(item.scheduled_at)}</time>
          <p>{item.body}</p>
          {item.read_at ? <span>Đã xác nhận</span> : <button onClick={() => action(async () => {
            await api(`/broadcasts/${item.id}/read`,"PUT");
            setAnnouncements(await api("/broadcasts"));
          })}>Xác nhận đã đọc</button>}
        </article>)}
      </section>}
      {panel === "chat" && (channel ? <>
        <header className="conversation-header">
          <Avatar name={channel.name} kind={channel.kind}/>
          <div><h1>{channel.name}</h1><p>{channelType[channel.kind]}</p></div>
          <button className="info-toggle" aria-expanded={infoOpen} onClick={() => setInfoOpen(!infoOpen)}>Thông tin</button>
        </header>
        <div className="chat-timeline" ref={timeline} onScroll={(event) => {
          const box=event.currentTarget;
          scrollState.current.follow=box.scrollHeight-box.scrollTop-box.clientHeight<160;
        }}>
        <div className="history-tools">
          {messages.length >= 50 && <button onClick={() => action(async () => {
            const older = await api<Message[]>(`/channels/${selected}/messages?before_seq=${messages[0].seq}`);
            if (active.current.selected === selected) setMessages((previous) => mergeMessages(previous,older));
          })}>Tải tin cũ</button>}
        </div>
        {messages.length === 0 && <div className="conversation-intro"><Avatar name={channel.name} kind={channel.kind}/><h2>{channel.name}</h2><p className="chat-muted">Chưa có tin nhắn.</p></div>}
        {messages.filter((message) => !message.parent_id).map(messageCard)}
        {thread && <section aria-label="Thảo luận">
          <h2>Thảo luận</h2><button onClick={() => { setThread(""); setReplies([]); }}>Đóng thảo luận</button>
          {replies.map(messageCard)}
        </section>}
        </div>
        {channel.kind !== "announcement" && <form className="chat-composer" onSubmit={sendForm}>
          <input className="visually-hidden" tabIndex={-1} aria-label="Tệp đính kèm" ref={fileInput} type="file" onChange={(event) => setAttachment(event.target.files?.[0] ?? null)} />
          <button className="attach-button" type="button" aria-label="Đính kèm tệp" title="Đính kèm tệp" onClick={() => fileInput.current?.click()}>＋</button>
          <textarea aria-label="Tin nhắn" placeholder={thread ? "Trả lời thảo luận…" : "Nhập tin nhắn…"} rows={1}
            value={draft} onChange={(event) => setDraft(event.target.value)} maxLength={16000}
            onKeyDown={(event) => {
              if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
                event.preventDefault();
                event.currentTarget.form?.requestSubmit();
              }
            }}/>
          <button className="chat-primary" disabled={busy || !!pending || (!draft.trim() && !attachment)}>Gửi</button>
          {attachment && <span className="attachment-name">{attachment.name} <button type="button" aria-label="Bỏ tệp" onClick={() => {
            setAttachment(null); if (fileInput.current) fileInput.current.value="";
          }}>×</button></span>}
          {pending && (pending.channel === selected
            ? <button type="button" disabled={busy} onClick={() => void send(pending)}>Gửi lại tin đang chờ</button>
            : <button type="button" onClick={() => open(pending.channel)}>Mở đoạn chat có tin đang chờ</button>)}
        </form>}
      </> : <div className="chat-blank"><span className="chat-avatar" aria-hidden="true">✦</span>
        <h1>Tin nhắn của bạn</h1><p className="chat-muted">Chọn một đoạn chat hoặc bắt đầu nhắn tin.</p>
        <button className="chat-primary" onClick={startConversation}>Tin nhắn mới</button>
      </div>)}
    </main>
    {infoOpen && channel && panel === "chat" && <aside className="chat-info" aria-label="Thông tin hội thoại">
      <button onClick={() => setInfoOpen(false)}>‹ Quay lại</button>
      <Avatar name={channel.name} kind={channel.kind}/>
      <h2>{channel.name}</h2>
      <h3>Tệp gần đây</h3>
      {recentFiles.length ? <ul className="shared-files">{recentFiles.map((file) => <li key={file.id}>
        <button onClick={() => void action(() => download("/files/"+file.id,file.name))}>{file.name}</button>
      </li>)}</ul> : <p className="chat-muted">Chưa có tệp trong các tin đã tải.</p>}
    </aside>}
  </div>;
}

createRoot(document.getElementById("root")!).render(<Session>{(user) => <Chat key={user.id} user={user}/>}</Session>);
