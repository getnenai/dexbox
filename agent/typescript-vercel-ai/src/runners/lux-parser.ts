export interface LuxAction {
  type: string;
  args: string;
  count: number;
}

export interface LuxStep {
  reason: string;
  actions: LuxAction[];
  stop: boolean;
  success: boolean;
}

export function parseLuxOutput(raw: string): LuxStep {
  // Extract reasoning
  const thinkMatch = raw.match(/<\|think_start\|>([\s\S]*?)<\|think_end\|>/);
  const reason = thinkMatch ? thinkMatch[1].trim() : "";

  // Extract action block
  const actionMatch = raw.match(
    /<\|action_start\|>([\s\S]*?)<\|action_end\|>/
  );
  const actionBlock = actionMatch ? actionMatch[1].trim() : "";

  if (!actionBlock) {
    return { reason, actions: [], stop: false, success: false };
  }

  // Split by & respecting parentheses
  const rawActions = splitActions(actionBlock);
  const actions: LuxAction[] = [];
  let stop = false;
  let success = false;

  for (const raw of rawActions) {
    const trimmed = raw.trim();
    if (!trimmed) continue;

    if (trimmed === "finish()" || trimmed === "finish") {
      stop = true;
      success = true;
      continue;
    }
    if (trimmed === "fail()" || trimmed === "fail") {
      stop = true;
      success = false;
      continue;
    }

    const parsed = parseAction(trimmed);
    if (parsed) actions.push(parsed);
  }

  return { reason, actions, stop, success };
}

function splitActions(block: string): string[] {
  const parts: string[] = [];
  let depth = 0;
  let current = "";

  for (const ch of block) {
    if (ch === "(") depth++;
    if (ch === ")") depth--;
    if (ch === "&" && depth === 0) {
      parts.push(current);
      current = "";
    } else {
      current += ch;
    }
  }
  if (current.trim()) parts.push(current);
  return parts;
}

function parseAction(raw: string): LuxAction | null {
  const match = raw.match(/^(\w+)\(([^)]*)\)$/s);
  if (!match) return null;

  const type = match[1];
  const args = match[2].trim();
  return { type, args, count: 1 };
}

// Scale a Lux normalized coordinate (0-1000) to screenshot pixel space.
function scaleX(v: number, w: number): number {
  return Math.round((v / 1000) * w);
}
function scaleY(v: number, h: number): number {
  return Math.round((v / 1000) * h);
}

export function luxActionToDexbox(
  action: LuxAction,
  screenshotW: number,
  screenshotH: number
): Record<string, unknown>[] | null {
  const { type, args } = action;

  switch (type) {
    case "click": {
      const [x, y] = parseCoords(args);
      return [
        {
          type: "computer_20250124",
          action: "left_click",
          coordinate: [scaleX(x, screenshotW), scaleY(y, screenshotH)],
        },
      ];
    }
    case "left_double": {
      const [x, y] = parseCoords(args);
      return [
        {
          type: "computer_20250124",
          action: "double_click",
          coordinate: [scaleX(x, screenshotW), scaleY(y, screenshotH)],
        },
      ];
    }
    case "left_triple": {
      const [x, y] = parseCoords(args);
      // No triple-click in dexbox; emulate with double_click + left_click
      return [
        {
          type: "computer_20250124",
          action: "double_click",
          coordinate: [scaleX(x, screenshotW), scaleY(y, screenshotH)],
        },
        {
          type: "computer_20250124",
          action: "left_click",
          coordinate: [scaleX(x, screenshotW), scaleY(y, screenshotH)],
        },
      ];
    }
    case "right_single": {
      const [x, y] = parseCoords(args);
      return [
        {
          type: "computer_20250124",
          action: "right_click",
          coordinate: [scaleX(x, screenshotW), scaleY(y, screenshotH)],
        },
      ];
    }
    case "type": {
      const text = stripQuotes(args);
      return [{ type: "computer_20250124", action: "type", text }];
    }
    case "hotkey": {
      const parts = args.split(",").map((s) => s.trim());
      const key = parts[0];
      const count = parts.length > 1 ? parseInt(parts[1], 10) || 1 : 1;
      const actions: Record<string, unknown>[] = [];
      for (let i = 0; i < count; i++) {
        actions.push({ type: "computer_20250124", action: "key", text: key });
      }
      return actions;
    }
    case "scroll": {
      const parts = args.split(",").map((s) => s.trim());
      const x = scaleX(parseInt(parts[0], 10), screenshotW);
      const y = scaleY(parseInt(parts[1], 10), screenshotH);
      const dir = parts[2] || "down";
      if (dir !== "up" && dir !== "down") {
        console.warn(`Unsupported scroll direction "${dir}"; skipping.`);
        return null;
      }
      const count = parts.length > 3 ? parseInt(parts[3], 10) || 1 : 1;
      const scrollDir = dir === "up" ? "scroll_up" : "scroll_down";
      const actions: Record<string, unknown>[] = [];
      for (let i = 0; i < count; i++) {
        actions.push({
          type: "computer_20250124",
          action: scrollDir,
          coordinate: [x, y],
        });
      }
      return actions;
    }
    case "drag": {
      const parts = args.split(",").map((s) => parseInt(s.trim(), 10));
      if (parts.length < 4 || parts.some(Number.isNaN)) {
        console.warn(`Invalid drag args: ${args}`);
        return null;
      }
      return [
        {
          type: "computer_20250124",
          action: "left_click_drag",
          start_coordinate: [scaleX(parts[0], screenshotW), scaleY(parts[1], screenshotH)],
          coordinate: [scaleX(parts[2], screenshotW), scaleY(parts[3], screenshotH)],
        },
      ];
    }
    case "wait":
      return null; // handled client-side
    default:
      console.warn(`Unknown Lux action: ${type}`);
      return null;
  }
}

// Lux coordinates are normalized 0-1000. These are scaled to screenshot
// dimensions at execution time via luxActionToDexbox's width/height params.
function parseCoords(args: string): [number, number] {
  const parts = args.split(",").map((s) => parseInt(s.trim(), 10));
  const x = Number.isNaN(parts[0]) ? 0 : parts[0];
  const y = Number.isNaN(parts[1]) ? 0 : parts[1];
  return [x, y];
}

function stripQuotes(s: string): string {
  const trimmed = s.trim();
  if (
    (trimmed.startsWith('"') && trimmed.endsWith('"')) ||
    (trimmed.startsWith("'") && trimmed.endsWith("'"))
  ) {
    return trimmed.slice(1, -1);
  }
  return trimmed;
}
