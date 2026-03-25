// Dynamically builds AI SDK tools from the dexbox server's GET /tools endpoint.
// Parameter schemas are fetched at startup and passed directly as JSON Schema
// (no Zod conversion needed since AI SDK v6 supports jsonSchema() natively).
import { tool, jsonSchema } from "ai";
import { callDexbox } from "./client.js";

const DEXBOX_URL = process.env.DEXBOX_URL || "http://localhost:8600";

// Schema types matching the server's GET /tools response.
interface ToolSchema {
  name: string;
  description: string;
  parameters: Record<string, unknown>;
  display_width_px?: number;
  display_height_px?: number;
}

// Anthropic tool type IDs used in POST /actions requests.
const TOOL_TYPE_IDS: Record<string, string> = {
  computer: "computer_20250124",
  bash: "bash_20250124",
};

// Build the execute function for a tool. Forwards args to POST /actions.
function makeExecute(toolName: string) {
  const typeId = TOOL_TYPE_IDS[toolName];

  return async (args: Record<string, unknown>) => {
    const body: Record<string, unknown> = { type: typeId };
    for (const [key, value] of Object.entries(args)) {
      if (value !== undefined) {
        body[key] = value;
      }
    }

    const res = await callDexbox(body);

    if (toolName === "computer") {
      if (args.action === "screenshot" && res.base64_image) {
        return { base64_image: res.base64_image as string };
      }
      if (args.action === "cursor_position" && res.coordinate) {
        return { coordinate: res.coordinate };
      }
      return { status: `executed ${args.action}` };
    }

    return { output: (res.output as string) || "" };
  };
}

// Fetch tool schemas from the server and build AI SDK tool definitions.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export async function loadTools(): Promise<Record<string, any>> {
  const res = await fetch(`${DEXBOX_URL}/tools`);
  if (!res.ok) {
    throw new Error(`Failed to fetch tool schemas: ${res.status}`);
  }

  const schemas: ToolSchema[] = await res.json();
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const tools: Record<string, any> = {};

  for (const schema of schemas) {
    const parameters = jsonSchema(schema.parameters);
    const execute = makeExecute(schema.name);

    if (schema.name === "computer") {
      tools[schema.name + "Tool"] = tool({
        description: schema.description,
        inputSchema: parameters,
        execute: execute as any,
        toModelOutput: ({ output }: any) => {
          if (output.base64_image) {
            return {
              type: "content" as const,
              value: [
                {
                  type: "image-data" as const,
                  data: output.base64_image,
                  mediaType: "image/png",
                },
              ],
            };
          }
          if (output.coordinate) {
            return {
              type: "text" as const,
              value: JSON.stringify(output.coordinate),
            };
          }
          return { type: "text" as const, value: output.status || "" };
        },
      } as any);
    } else {
      tools[schema.name + "Tool"] = tool({
        description: schema.description,
        inputSchema: parameters,
        execute: execute as any,
      } as any);
    }
  }

  return tools;
}
