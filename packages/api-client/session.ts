export interface User {
  id: string;
  username: string;
  display_name: string;
  admin: boolean;
}

export class ApiError extends Error {
  constructor(public status: number, public code: string) { super(code); }
}

let access = "";
let refreshTask: Promise<void> | undefined;
const listeners = new Set<() => void>();
const tabs = new BroadcastChannel("chattp-session");

function clear() {
  access = "";
  listeners.forEach((handler) => handler());
}

tabs.onmessage = (event) => {
  if (event.data === "logout") clear();
};

export function onLogout(handler: () => void) {
  listeners.add(handler);
  return () => { listeners.delete(handler); };
}

async function request<T>(path: string, method = "GET", payload?: unknown): Promise<T> {
  const headers: Record<string, string> = { "X-Chat-Client": "web" };
  if (access) headers.Authorization = "Bearer " + access;
  if (payload !== undefined && !(payload instanceof FormData)) headers["Content-Type"] = "application/json";
  const response = await fetch("/v1" + path, {
    method, headers, credentials: "same-origin", cache: "no-store",
    body: payload instanceof FormData ? payload : payload === undefined ? undefined : JSON.stringify(payload),
  });
  if (!response.ok) {
    const body = await response.json().catch(() => null);
    throw new ApiError(response.status, body?.error?.code ?? "request_failed");
  }
  return response.status === 204 ? undefined as T : response.json();
}

export async function fileBlob(path: string, signal?: AbortSignal) {
  const fetchFile = () => fetch("/v1" + path, {headers:{Authorization:"Bearer " + access},cache:"no-store",signal});
  let response = await fetchFile();
  if (response.status === 401) { await refresh(); response = await fetchFile(); }
  if (!response.ok) throw new ApiError(response.status,"file_unavailable");
  return response.blob();
}

export async function download(path: string, name: string) {
  const url = URL.createObjectURL(await fileBlob(path));
  const link = document.createElement("a");
  link.href = url;
  link.download = name;
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 2000);
}

export function refresh() {
  if (!refreshTask) {
    const work = async () => {
      const result = await request<{ access_token: string }>("/auth/refresh", "POST", {});
      access = result.access_token;
    };
    refreshTask = (navigator.locks ? navigator.locks.request("chattp-refresh", work) : work())
      .finally(() => { refreshTask = undefined; });
  }
  return refreshTask;
}

export function socketToken() { return access; }

export async function api<T>(path: string, method = "GET", payload?: unknown) {
  try {
    return await request<T>(path, method, payload);
  } catch (error) {
    if (!(error instanceof ApiError) || error.status !== 401) throw error;
    try {
      await refresh();
    } catch (failure) {
      if (failure instanceof ApiError && failure.status === 401) clear();
      throw failure;
    }
    return request<T>(path, method, payload);
  }
}

export async function login(username: string, password: string) {
  const result = await request<{ access_token: string }>("/auth/login", "POST", { username, password });
  access = result.access_token;
  return api<User>("/me");
}

export async function logout() {
  await api("/auth/logout", "POST");
  clear();
  tabs.postMessage("logout");
}

