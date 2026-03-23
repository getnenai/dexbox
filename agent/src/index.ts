import "dotenv/config";
import { generateText, stepCountIs } from "ai";
import { anthropic } from "@ai-sdk/anthropic";
import { loadTools } from "./tools/loader.js";

const SYSTEM_PROMPT = `You are a computer-use agent controlling a Windows 11 virtual machine.

You have three tools:
- computerTool: Take screenshots, click, type, scroll, and drag on the VM screen.
- bashTool: Run PowerShell commands on the guest (not Linux bash, use PowerShell syntax).
- text_editorTool: View and edit files on the guest using Windows paths.

Guidelines:
- Always start by taking a screenshot to see the current state of the desktop.
- After every significant action (click, type, command), take a screenshot to verify the result.
- Prefer PowerShell (bashTool) for tasks that can be done programmatically, it is faster and more reliable than GUI interaction.
- Use Windows file paths (e.g. C:\\Users\\dexbox\\Desktop\\file.txt).
- For GUI interaction, identify UI elements from screenshots and click their coordinates precisely.
- If an action does not produce the expected result, try an alternative approach.
- When the task is complete, summarize what was accomplished.`;

async function main() {
  const prompt = process.argv.slice(2).join(" ");
  if (!prompt) {
    console.error("Usage: npx tsx src/index.ts <prompt>");
    console.error(
      'Example: npx tsx src/index.ts "Take a screenshot of the desktop"'
    );
    process.exit(1);
  }

  if (!process.env.ANTHROPIC_API_KEY) {
    console.error(
      "ANTHROPIC_API_KEY is not set. Create a .env file from .env.example."
    );
    process.exit(1);
  }

  const baseUrl = process.env.DEXBOX_URL || "http://localhost:8600";
  try {
    const health = await fetch(`${baseUrl}/health`);
    if (!health.ok) throw new Error(`status ${health.status}`);
    console.log(`Connected to dexbox at ${baseUrl}`);
  } catch {
    console.error(
      `Cannot reach dexbox at ${baseUrl}. Is 'dexbox start' running?`
    );
    process.exit(1);
  }

  // Load tool definitions dynamically from the dexbox server.
  const tools = await loadTools();
  console.log(`Loaded tools: ${Object.keys(tools).join(", ")}`);
  console.log(`Running: "${prompt}"\n`);

  const result = await generateText({
    model: anthropic("claude-sonnet-4-6") as any,
    tools,
    stopWhen: stepCountIs(30),
    system: SYSTEM_PROMPT,
    prompt,
    onStepFinish: ({ toolCalls, toolResults, text }: any) => {
      if (toolCalls?.length) {
        for (const tc of toolCalls) {
          const input = JSON.stringify(tc.input ?? tc.args, null, 2);
          console.log(`  [call] ${tc.toolName}: ${input}`);
        }
      }
      if (toolResults?.length) {
        for (const tr of toolResults) {
          const out = tr.output ?? tr.result;
          const preview = out?.base64_image
            ? `<screenshot ${out.base64_image.length} chars>`
            : JSON.stringify(out).slice(0, 200);
          console.log(`  [result] ${tr.toolName}: ${preview}`);
        }
      }
      if (text) {
        console.log(`  [text] ${text.slice(0, 200)}`);
      }
    },
  });

  console.log("\n--- Result ---");
  console.log(result.text);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
