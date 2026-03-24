import { generateText, stepCountIs } from "ai";
import { anthropic } from "@ai-sdk/anthropic";
import { loadTools } from "../tools/loader.js";

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

export async function runClaude(prompt: string): Promise<string> {
  const tools = await loadTools();
  console.log(`Loaded tools: ${Object.keys(tools).join(", ")}`);
  console.log(`Running with Claude: "${prompt}"\n`);

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

  return result.text;
}
