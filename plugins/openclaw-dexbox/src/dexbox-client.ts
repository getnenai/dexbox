/**
 * Thin HTTP client for the dexbox server API.
 * All functions throw on non-2xx responses with descriptive error messages.
 */

const DEFAULT_BASE_URL = "http://localhost:8600";
const TIMEOUT_MS = 30_000;

/** Send an HTTP request to the dexbox server and return the response body as a string. */
export async function doRequest(
  baseUrl: string | undefined,
  method: string,
  path: string,
  body?: Record<string, unknown>,
): Promise<string> {
  const url = (baseUrl || DEFAULT_BASE_URL) + path;
  const init: RequestInit = {
    method,
    signal: AbortSignal.timeout(TIMEOUT_MS),
  };
  if (body) {
    init.headers = { "Content-Type": "application/json" };
    init.body = JSON.stringify(body);
  }

  let resp: Response;
  try {
    resp = await fetch(url, init);
  } catch (err) {
    throw new Error(
      `dexbox server unreachable at ${baseUrl || DEFAULT_BASE_URL} (is 'dexbox start' running?): ${err}`,
    );
  }

  const text = await resp.text();
  if (!resp.ok) {
    throw new Error(`dexbox API error (HTTP ${resp.status}): ${text}`);
  }
  return text;
}

/**
 * Send an action tool call to POST /actions and return the raw response.
 * Uses model=claude-mcp to route through the Anthropic adapter.
 */
export async function doAction(
  baseUrl: string | undefined,
  desktop: string | undefined,
  actionBody: Record<string, unknown>,
): Promise<string> {
  const qs = new URLSearchParams({ model: "claude-mcp" });
  if (desktop) qs.set("desktop", desktop);

  const url = `${baseUrl || DEFAULT_BASE_URL}/actions?${qs}`;
  let resp: Response;
  try {
    resp = await fetch(url, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(actionBody),
      signal: AbortSignal.timeout(TIMEOUT_MS),
    });
  } catch (err) {
    throw new Error(
      `dexbox server unreachable at ${baseUrl || DEFAULT_BASE_URL} (is 'dexbox start' running?): ${err}`,
    );
  }

  const text = await resp.text();
  if (!resp.ok) {
    throw new Error(`dexbox API error (HTTP ${resp.status}): ${text}`);
  }
  return text;
}

/**
 * Take a screenshot and return base64-encoded PNG data.
 *
 * Note: "computer_20250124" / "bash_20250124" and model=claude-mcp are internal
 * wire format identifiers used by the dexbox server's Anthropic adapter. They
 * are not model-specific -- the dexbox server translates them into its own
 * actions regardless of which model the caller is using.
 */
export async function doScreenshot(
  baseUrl: string | undefined,
  desktop: string | undefined,
): Promise<string> {
  const qs = new URLSearchParams({ model: "claude-mcp" });
  if (desktop) qs.set("desktop", desktop);

  const url = `${baseUrl || DEFAULT_BASE_URL}/actions?${qs}`;
  let resp: Response;
  try {
    resp = await fetch(url, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Accept: "image/png",
      },
      body: JSON.stringify({ type: "computer_20250124", action: "screenshot" }),
      signal: AbortSignal.timeout(TIMEOUT_MS),
    });
  } catch (err) {
    throw new Error(
      `dexbox server unreachable at ${baseUrl || DEFAULT_BASE_URL} (is 'dexbox start' running?): ${err}`,
    );
  }

  if (!resp.ok) {
    const text = await resp.text();
    throw new Error(`dexbox API error (HTTP ${resp.status}): ${text}`);
  }

  const buf = await resp.arrayBuffer();
  return Buffer.from(buf).toString("base64");
}
