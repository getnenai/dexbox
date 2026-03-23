const DEXBOX_URL = process.env.DEXBOX_URL || "http://localhost:8600";
const DEXBOX_VM = process.env.DEXBOX_VM || "";

export async function callDexbox(
  action: Record<string, unknown>
): Promise<Record<string, unknown>> {
  const params = new URLSearchParams({
    model: "claude-sonnet-4-20250514",
  });
  if (DEXBOX_VM) {
    params.set("vm", DEXBOX_VM);
  }

  const res = await fetch(`${DEXBOX_URL}/actions?${params}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(action),
  });

  const body = await res.json();

  if (!res.ok) {
    const msg = (body as Record<string, unknown>).message || res.statusText;
    throw new Error(`dexbox ${res.status}: ${msg}`);
  }

  return body as Record<string, unknown>;
}
