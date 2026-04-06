import { doRequest, doAction, doScreenshot } from "./src/dexbox-client.js";

type PluginApi = {
  pluginConfig: { baseUrl?: string };
  registerTool(def: {
    name: string;
    description: string;
    parameters: Record<string, unknown>;
    execute: (toolCallId: string, params: Record<string, any>) => Promise<{
      content: Array<{ type: string; text?: string; data?: string; mimeType?: string }>;
    }>;
  }): void;
};

function text(value: string) {
  return { content: [{ type: "text" as const, text: value }] };
}

function image(base64: string) {
  return {
    content: [{ type: "image" as const, data: base64, mimeType: "image/png" }],
  };
}

function registerDexboxTools(api: PluginApi) {
  const baseUrl = api.pluginConfig.baseUrl;

  // --- Lifecycle tools ---

  api.registerTool({
    name: "list_desktops",
    description:
      "List all dexbox desktops (VMs and RDP connections) with their current state.",
    parameters: {
      type: "object",
      properties: {
        type: {
          type: "string",
          enum: ["vm", "rdp"],
          description: "Filter by desktop type. Omit to list all.",
        },
      },
    },
    async execute(_id, params) {
      const path = params.type ? `/desktops?type=${params.type}` : "/desktops";
      return text(await doRequest(baseUrl, "GET", path));
    },
  });

  api.registerTool({
    name: "create_desktop",
    description: "Register a new RDP desktop connection.",
    parameters: {
      type: "object",
      properties: {
        name: { type: "string", description: "Unique name for the desktop" },
        type: { type: "string", description: "Desktop type (currently only rdp)" },
        host: { type: "string", description: "RDP host address" },
        port: { type: "integer", description: "RDP port (default 3389)" },
        username: { type: "string", description: "RDP username" },
        password: { type: "string", description: "RDP password" },
      },
      required: ["name", "host", "username", "password"],
    },
    async execute(_id, params) {
      return text(await doRequest(baseUrl, "POST", "/desktops", params));
    },
  });

  api.registerTool({
    name: "destroy_desktop",
    description: "Destroy a desktop (delete VM or unregister RDP connection).",
    parameters: {
      type: "object",
      properties: {
        name: { type: "string", description: "Desktop name" },
      },
      required: ["name"],
    },
    async execute(_id, params) {
      return text(
        await doRequest(baseUrl, "DELETE", `/desktops/${encodeURIComponent(params.name)}`),
      );
    },
  });

  api.registerTool({
    name: "start_desktop",
    description: "Bring a desktop online (boot VM or connect RDP session).",
    parameters: {
      type: "object",
      properties: {
        name: { type: "string", description: "Desktop name" },
      },
      required: ["name"],
    },
    async execute(_id, params) {
      return text(
        await doRequest(
          baseUrl,
          "POST",
          `/desktops/${encodeURIComponent(params.name)}?action=up`,
        ),
      );
    },
  });

  api.registerTool({
    name: "stop_desktop",
    description: "Disconnect a desktop session and shut down the VM guest OS.",
    parameters: {
      type: "object",
      properties: {
        name: { type: "string", description: "Desktop name" },
        force: {
          type: "boolean",
          description: "Hard poweroff instead of graceful shutdown (VM only)",
        },
      },
      required: ["name"],
    },
    async execute(_id, params) {
      const qs = new URLSearchParams({ action: "down", shutdown: "true" });
      if (params.force) qs.set("force", "true");
      return text(
        await doRequest(
          baseUrl,
          "POST",
          `/desktops/${encodeURIComponent(params.name)}?${qs}`,
        ),
      );
    },
  });

  api.registerTool({
    name: "status_desktop",
    description: "Get the current status of a single desktop.",
    parameters: {
      type: "object",
      properties: {
        name: { type: "string", description: "Desktop name" },
      },
      required: ["name"],
    },
    async execute(_id, params) {
      return text(
        await doRequest(baseUrl, "GET", `/desktops/${encodeURIComponent(params.name)}`),
      );
    },
  });

  // --- Action tools (computer-use) ---

  api.registerTool({
    name: "screenshot",
    description: "Take a screenshot of the desktop. Returns a PNG image.",
    parameters: {
      type: "object",
      properties: {
        desktop: {
          type: "string",
          description: "Desktop name. Omit if only one desktop is connected.",
        },
      },
    },
    async execute(_id, params) {
      return image(await doScreenshot(baseUrl, params.desktop));
    },
  });

  api.registerTool({
    name: "click",
    description:
      "Click at a coordinate on the desktop. Use button to specify left (default), right, middle, or double click.",
    parameters: {
      type: "object",
      properties: {
        desktop: {
          type: "string",
          description: "Desktop name. Omit if only one desktop is connected.",
        },
        x: { type: "integer", description: "X coordinate" },
        y: { type: "integer", description: "Y coordinate" },
        button: {
          type: "string",
          enum: ["left", "right", "middle", "double"],
          description: "Mouse button (default: left)",
        },
      },
      required: ["x", "y"],
    },
    async execute(_id, params) {
      const actionMap: Record<string, string> = {
        left: "left_click",
        right: "right_click",
        middle: "middle_click",
        double: "double_click",
      };
      const action = actionMap[params.button || "left"] || "left_click";
      await doAction(baseUrl, params.desktop, {
        type: "computer_20250124",
        action,
        coordinate: [params.x, params.y],
      });
      return text(`clicked (${params.x}, ${params.y})`);
    },
  });

  api.registerTool({
    name: "type_text",
    description: "Type text on the desktop. Click on the target input field first.",
    parameters: {
      type: "object",
      properties: {
        desktop: {
          type: "string",
          description: "Desktop name. Omit if only one desktop is connected.",
        },
        text: { type: "string", description: "Text to type" },
      },
      required: ["text"],
    },
    async execute(_id, params) {
      await doAction(baseUrl, params.desktop, {
        type: "computer_20250124",
        action: "type",
        text: params.text,
      });
      return text("typed text");
    },
  });

  api.registerTool({
    name: "key_press",
    description: "Press a key or key combo (e.g. enter, ctrl+c, alt+tab, shift+F5).",
    parameters: {
      type: "object",
      properties: {
        desktop: {
          type: "string",
          description: "Desktop name. Omit if only one desktop is connected.",
        },
        key: { type: "string", description: "Key or combo to press" },
      },
      required: ["key"],
    },
    async execute(_id, params) {
      await doAction(baseUrl, params.desktop, {
        type: "computer_20250124",
        action: "key",
        text: params.key,
      });
      return text(`pressed ${params.key}`);
    },
  });

  api.registerTool({
    name: "scroll",
    description: "Scroll at a coordinate on the desktop.",
    parameters: {
      type: "object",
      properties: {
        desktop: {
          type: "string",
          description: "Desktop name. Omit if only one desktop is connected.",
        },
        x: { type: "integer", description: "X coordinate to scroll at" },
        y: { type: "integer", description: "Y coordinate to scroll at" },
        direction: {
          type: "string",
          enum: ["up", "down"],
          description: "Scroll direction (default: down)",
        },
        amount: {
          type: "integer",
          description: "Scroll amount in lines (default: 3)",
        },
      },
      required: ["x", "y"],
    },
    async execute(_id, params) {
      const body: Record<string, unknown> = {
        type: "computer_20250124",
        action: "scroll",
        coordinate: [params.x, params.y],
      };
      if (params.direction) body.direction = params.direction;
      if (params.amount) body.amount = params.amount;
      await doAction(baseUrl, params.desktop, body);
      return text("scrolled");
    },
  });

  api.registerTool({
    name: "bash",
    description:
      "Run a PowerShell command on the desktop guest OS. Returns the command output.",
    parameters: {
      type: "object",
      properties: {
        desktop: {
          type: "string",
          description: "Desktop name. Omit if only one desktop is connected.",
        },
        command: {
          type: "string",
          description: "PowerShell command to execute in the guest VM",
        },
      },
      required: ["command"],
    },
    async execute(_id, params) {
      const resp = await doAction(baseUrl, params.desktop, {
        type: "bash_20250124",
        command: params.command,
      });
      try {
        const parsed = JSON.parse(resp);
        return text(parsed.output || resp);
      } catch {
        return text(resp);
      }
    },
  });
}

// Plugin entry point — use the OpenClaw definePluginEntry pattern.
// The actual import path depends on the OpenClaw SDK version installed;
// this file is structured so it can be wrapped by definePluginEntry at
// publish time or imported directly by the OpenClaw plugin loader.
export default {
  id: "dexbox",
  name: "Dexbox Desktop Control",
  description: "Control Windows VMs and RDP desktops via dexbox",
  register(api: PluginApi) {
    registerDexboxTools(api);
  },
};
