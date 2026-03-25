import { readFileSync, mkdirSync, writeFileSync } from "fs";
import { resolve, basename, dirname, join } from "path";
import { homedir } from "os";

const API_BASE = "https://api.extend.ai";
const API_VERSION = "2026-02-09";

export interface ExtendParseResult {
  raw: Record<string, unknown>;
  markdown: string;
  outputPath: string;
}

export async function parseDocument(
  filePath: string,
  options?: { apiKey?: string; sharedDir?: string },
): Promise<ExtendParseResult> {
  const apiKey = options?.apiKey ?? process.env.EXTEND_API_KEY;
  if (!apiKey) {
    throw new Error(
      "EXTEND_API_KEY is not set. Provide it via env or options.apiKey.",
    );
  }

  const rawSharedDir =
    options?.sharedDir ??
    process.env.DEXBOX_SHARED_DIR ??
    join(homedir(), ".dexbox", "shared");
  const sharedDir = rawSharedDir.startsWith("~")
    ? join(homedir(), rawSharedDir.slice(1))
    : rawSharedDir;

  const absPath = resolve(filePath.startsWith("/") ? filePath : join(sharedDir, filePath));
  const sharedRoot = resolve(sharedDir);
  if (!absPath.startsWith(sharedRoot + "/") && absPath !== sharedRoot) {
    throw new Error(`Invalid file path (outside shared dir): ${filePath}`);
  }

  let fileBytes: Buffer;
  try {
    fileBytes = readFileSync(absPath);
  } catch {
    throw new Error(`File not found: ${absPath}`);
  }

  const headers = {
    Authorization: `Bearer ${apiKey}`,
    "x-extend-api-version": API_VERSION,
  };

  // Step 1: Upload
  const form = new FormData();
  form.append("file", new Blob([new Uint8Array(fileBytes)]), basename(absPath));

  const uploadRes = await fetch(`${API_BASE}/files/upload`, {
    method: "POST",
    headers,
    body: form,
    signal: AbortSignal.timeout(300_000),
  });

  if (!uploadRes.ok) {
    const body = await uploadRes.text();
    throw new Error(`Upload failed (${uploadRes.status}): ${body}`);
  }

  const uploadJson = (await uploadRes.json()) as { id: string };
  const fileId = uploadJson.id;

  // Step 2: Parse
  const parseRes = await fetch(`${API_BASE}/parse`, {
    method: "POST",
    headers: { ...headers, "Content-Type": "application/json" },
    body: JSON.stringify({ file: { id: fileId } }),
    signal: AbortSignal.timeout(300_000),
  });

  if (!parseRes.ok) {
    const body = await parseRes.text();
    throw new Error(`Parse failed (${parseRes.status}): ${body}`);
  }

  const raw = (await parseRes.json()) as Record<string, unknown>;

  const output = (raw.output ?? {}) as Record<string, unknown>;
  const chunks = ((output.chunks ?? []) as Array<Record<string, unknown>>);
  const markdown = chunks
    .filter((c) => typeof c.content === "string" && c.content)
    .map((c) => c.content as string)
    .join("\n\n");

  // Write output
  const parsedDir = join(sharedDir, "parsed");
  mkdirSync(parsedDir, { recursive: true });
  const outputPath = join(parsedDir, `${basename(absPath)}.json`);
  writeFileSync(outputPath, JSON.stringify(raw, null, 2));

  return { raw, markdown, outputPath };
}

// CLI entry point
const scriptPath = resolve(process.argv[1] ?? "");
const thisPath = resolve(
  dirname(new URL(import.meta.url).pathname),
  basename(new URL(import.meta.url).pathname),
);

if (scriptPath === thisPath) {
  const file = process.argv[2];
  if (!file) {
    console.error("Usage: npx tsx extend-parse.ts <filename>");
    process.exit(1);
  }

  // Load dotenv from agent/.env (one dir up from shared/)
  try {
    // @ts-expect-error dotenv resolved from agent's node_modules at runtime
    const { config } = await import("dotenv");
    config({ path: resolve(dirname(new URL(import.meta.url).pathname), "..", ".env") });
  } catch {
    // dotenv not available, rely on env vars
  }

  try {
    const result = await parseDocument(file);
    console.log(result.markdown);
  } catch (err) {
    console.error(err instanceof Error ? err.message : err);
    process.exit(1);
  }
}
