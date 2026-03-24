import { randomUUID } from "crypto";
import { callDexbox } from "../tools/client.js";
import { parseLuxOutput, luxActionToDexbox } from "./lux-parser.js";

type LuxModel = "lux-actor-1" | "lux-thinker-1";

const LUX_BASE_URL = "https://api.agiopen.org/v1";

const MAX_STEPS: Record<LuxModel, number> = {
  "lux-actor-1": 50,
  "lux-thinker-1": 100,
};

interface LuxMessage {
  role: "user" | "assistant";
  content: string | Array<{ type: string; text?: string; image_url?: { url: string } }>;
}


function buildPrompt(task: string): string {
  return `You are a Desktop Agent completing computer use tasks from a user instruction.

Every step, you will look at the screenshot and output the desired actions in a format as:

<|think_start|> brief description of your intent and reasoning <|think_end|>
<|action_start|> one of the allowed actions as below <|action_end|>

In the action field, you have the following action formats:
1. click(x, y) # left-click at the position (x, y), where x and y are integers normalized between 0 and 1000
2. left_double(x, y) # left-double-click at the position (x, y), where x and y are integers normalized between 0 and 1000
3. left_triple(x, y) # left-triple-click at the position (x, y), where x and y are integers normalized between 0 and 1000
4. right_single(x, y) # right-click at the position (x, y), where x and y are integers normalized between 0 and 1000
5. drag(x1, y1, x2, y2) # drag the mouse from (x1, y1) to (x2, y2) to select or move contents, where x1, y1, x2, y2 are integers normalized between 0 and 1000
6. hotkey(key, c) # press the key for c times
7. type(text) # type a text string on the keyboard
8. scroll(x, y, direction, c) # scroll the mouse at position (x, y) in the direction of up or down for c times, where x and y are integers normalized between 0 and 1000
9. wait() # wait for a while
10. finish() # indicate the task is finished

Directly output the text beginning with <|think_start|>, no additional text is needed for this scenario.

The user instruction is:
${task}`;
}

async function uploadScreenshot(base64: string): Promise<string> {
  const apiKey = process.env.OAGI_API_KEY!;
  const buf = Buffer.from(base64, "base64");

  const uploadRes = await fetch(`${LUX_BASE_URL}/file/upload`, {
    method: "GET",
    headers: { "x-api-key": apiKey },
  });

  if (!uploadRes.ok) {
    throw new Error(`File upload failed: ${uploadRes.status}`);
  }

  const uploadData = (await uploadRes.json()) as {
    url: string;
    uuid: string;
    download_url: string;
  };

  const putRes = await fetch(uploadData.url, {
    method: "PUT",
    body: buf,
  });

  if (!putRes.ok) {
    throw new Error(`S3 upload failed: ${putRes.status}`);
  }

  return uploadData.download_url;
}

async function chatCompletion(
  model: LuxModel,
  messages: LuxMessage[],
  taskId: string
): Promise<string> {
  const apiKey = process.env.OAGI_API_KEY!;

  const payload = { model, messages, task_id: taskId };

  const res = await fetch(`${LUX_BASE_URL}/chat/completions`, {
    method: "POST",
    headers: {
      "x-api-key": apiKey,
      "Content-Type": "application/json",
    },
    body: JSON.stringify(payload),
  });

  if (!res.ok) {
    const body = await res.text();
    throw new Error(`Lux API ${res.status}: ${body}`);
  }

  const data = (await res.json()) as {
    choices: Array<{ message: { content: string } }>;
  };
  return data.choices[0]?.message?.content || "";
}

async function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function retryOnce<T>(fn: () => Promise<T>): Promise<T> {
  try {
    return await fn();
  } catch (err) {
    console.warn(`  [retry] ${err instanceof Error ? err.message : err}`);
    await sleep(2000);
    return await fn();
  }
}

async function getScreenshotDimensions(): Promise<{ width: number; height: number }> {
  const baseUrl = process.env.DEXBOX_URL || "http://localhost:8600";
  const res = await fetch(`${baseUrl}/tools`);
  const tools = (await res.json()) as Array<{
    name: string;
    display_width_px?: number;
    display_height_px?: number;
  }>;
  const computer = tools.find((t) => t.name === "computer");
  return {
    width: computer?.display_width_px || 1024,
    height: computer?.display_height_px || 768,
  };
}

export async function runLux(
  prompt: string,
  model: LuxModel
): Promise<string> {
  const taskId = randomUUID();
  const maxSteps = MAX_STEPS[model];
  const messages: LuxMessage[] = [];
  const { width: screenshotW, height: screenshotH } =
    await getScreenshotDimensions();

  console.log(`Running with ${model}: "${prompt}"\n`);

  for (let step = 1; step <= maxSteps; step++) {
    // 1. Take screenshot
    const screenshotRes = await callDexbox({
      type: "computer_20250124",
      action: "screenshot",
    });
    const base64 = screenshotRes.base64_image as string;

    // 2. Upload screenshot
    const downloadUrl = await retryOnce(() => uploadScreenshot(base64));

    // 3. Build message
    if (step === 1) {
      messages.push({
        role: "user",
        content: [
          { type: "text", text: buildPrompt(prompt) },
          { type: "image_url", image_url: { url: downloadUrl } },
        ],
      });
    } else {
      messages.push({
        role: "user",
        content: [
          { type: "image_url", image_url: { url: downloadUrl } },
        ],
      });
    }

    // 4. Call Lux API
    const responseText = await retryOnce(() =>
      chatCompletion(model, messages, taskId)
    );
    messages.push({ role: "assistant", content: responseText });

    // 5. Parse response
    const luxStep = parseLuxOutput(responseText);

    console.log(`[step ${step}]`);
    if (luxStep.reason) {
      console.log(`  [reason] ${luxStep.reason.slice(0, 300)}`);
    }

    // 6. Check stop conditions
    if (luxStep.stop) {
      const status = luxStep.success ? "FINISHED" : "FAILED";
      console.log(`  [${status}]`);
      return luxStep.success
        ? `Task completed successfully.\n\nFinal reasoning: ${luxStep.reason}`
        : `Task failed.\n\nFinal reasoning: ${luxStep.reason}`;
    }

    // 7. Execute actions
    for (const action of luxStep.actions) {
      if (action.type === "wait") {
        console.log(`  [action] wait(1s)`);
        await sleep(1000);
        continue;
      }

      const dexboxActions = luxActionToDexbox(action, screenshotW, screenshotH);
      if (!dexboxActions) continue;

      for (const dexboxAction of dexboxActions) {
        console.log(
          `  [action] ${action.type}(${action.args}) -> ${JSON.stringify(dexboxAction)}`
        );
        try {
          await callDexbox(dexboxAction);
        } catch (err) {
          console.error(
            `  [error] ${err instanceof Error ? err.message : err}`
          );
        }
      }
    }
  }

  return `Reached maximum steps (${maxSteps}) without completion.`;
}
