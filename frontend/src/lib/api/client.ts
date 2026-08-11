// Typed fetch wrapper. Every non-2xx response carries the backend's error
// envelope; ApiError surfaces its code/message/pveMessage to the UI so real
// Proxmox errors are always shown verbatim.

export interface ApiErrorBody {
  code: string;
  message: string;
  pveMessage?: string;
}

export class ApiError extends Error {
  readonly code: string;
  readonly status: number;
  readonly pveMessage?: string;

  constructor(status: number, body: ApiErrorBody) {
    super(body.message || `HTTP ${status}`);
    this.name = "ApiError";
    this.status = status;
    this.code = body.code || "unknown";
    this.pveMessage = body.pveMessage;
  }

  /** Full detail for toasts: message plus the verbatim Proxmox error. */
  get detail(): string {
    return this.pveMessage ? `${this.message} — ${this.pveMessage}` : this.message;
  }
}

export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: {
      ...(init?.body ? { "Content-Type": "application/json" } : {}),
      ...init?.headers,
    },
  });

  if (res.status === 401 && typeof window !== "undefined" && !path.startsWith("/api/auth/")) {
    window.location.href = "/signin";
  }

  if (!res.ok) {
    let body: ApiErrorBody = { code: "unknown", message: `HTTP ${res.status}` };
    try {
      const parsed = (await res.json()) as { error?: ApiErrorBody };
      if (parsed.error) body = parsed.error;
    } catch {
      // non-JSON error body — keep the fallback
    }
    throw new ApiError(res.status, body);
  }

  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}
