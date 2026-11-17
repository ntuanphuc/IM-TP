import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { api, login, logout, onLogout, refresh, type User } from "./session";

export function Session({ children, admin = false }: {children: (user: User) => ReactNode; admin?: boolean}) {
  const [user, setUser] = useState<User | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    let active = true;
    const unsubscribe = onLogout(() => { if (active) setUser(null); });
    refresh().then(() => api<User>("/me")).then((value) => { if (active) setUser(value); })
      .catch(() => {}).finally(() => { if (active) setLoading(false); });
    return () => { active = false; unsubscribe(); };
  }, []);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const values = new FormData(form);
    setBusy(true);
    setError("");
    try {
      setUser(await login(String(values.get("username")), String(values.get("password"))));
      form.reset();
    } catch {
      setError("Could not sign in. Check your username, password, and connection.");
    } finally {
      setBusy(false);
    }
  }

  if (loading) return <main className="login">Restoring session…</main>;
  if (!user) return <main className="login"><h1>{admin ? "ChatTP Admin" : "ChatTP"}</h1>
    <form onSubmit={submit}>
      <label>Username<input name="username" autoComplete="username" required /></label>
      <label>Password<input name="password" type="password" autoComplete="current-password" required /></label>
      <button disabled={busy}>{busy ? "Signing in…" : "Sign in"}</button>
    </form>
    {error && <p role="alert">{error}</p>}
  </main>;
  return <><header className="topbar"><strong>{admin ? "ChatTP Admin" : "ChatTP"}</strong>
    <span>{user.display_name}</span>
    <button onClick={() => { setError(""); logout().catch(() => setError("Could not sign out. Try again.")); }}>Sign out</button>
  </header>{error && <p role="alert">{error}</p>}
  {admin && !user.admin ? <main className="login"><p>Your account does not have admin access.</p></main> : children(user)}</>;
}
