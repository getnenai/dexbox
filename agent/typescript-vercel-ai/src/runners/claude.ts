import { generateText, stepCountIs, tool, jsonSchema } from "ai";
import { anthropic } from "@ai-sdk/anthropic";
import { loadTools } from "../tools/loader.js";
import { parseDocument } from "../tools/extend.js";

const PARSE_TOOL_DESCRIPTION =
  "Parse a document (PDF, image, etc.) using Extend AI and return its content as structured markdown text. " +
  "Use this to read and understand the contents of document files. " +
  "This tool runs on the host machine, NOT inside the Windows VM. " +
  "The shared folder between host and guest is the default lookup directory: " +
  "files at \\\\vboxsvr\\shared\\ on the Windows VM are in this directory. " +
  "Pass just the filename (e.g. 'invoice.pdf') for files in the shared folder, " +
  "or an absolute host path for files elsewhere.";

const BASE_SYSTEM_PROMPT = `You are a computer-use agent controlling a Windows 11 virtual machine.

You have two tools:
- computerTool: Take screenshots, click, type, scroll, and drag on the VM screen.
- bashTool: Run PowerShell commands on the guest (not Linux bash, use PowerShell syntax).

Guidelines:
- Always start by taking a screenshot to see the current state of the desktop.
- After every significant action (click, type, command), take a screenshot to verify the result.
- Prefer PowerShell (bashTool) for tasks that can be done programmatically, it is faster and more reliable than GUI interaction.
- Use Windows file paths (e.g. C:\\Users\\dexbox\\Desktop\\file.txt).
- For GUI interaction, identify UI elements from screenshots and click their coordinates precisely.
- If an action does not produce the expected result, try an alternative approach.
- When the task is complete, summarize what was accomplished.`;

const PARSE_TOOL_ADDENDUM = `
- parse_documentTool: Parse a document file (PDF, image, etc.) and return its content as markdown. This tool runs on the host, not inside the VM. Files in the VM's \\\\vboxsvr\\shared\\ folder are in the tool's default lookup directory — just pass the filename.`;

export async function runClaude(prompt: string): Promise<string> {
  const tools = await loadTools();

  const hasExtend = !!process.env.EXTEND_API_KEY;
  let systemPrompt = BASE_SYSTEM_PROMPT;

  if (hasExtend) {
    tools.parse_documentTool = tool({
      description: PARSE_TOOL_DESCRIPTION,
      inputSchema: jsonSchema({
        type: "object" as const,
        properties: {
          file_path: {
            type: "string",
            description:
              "Filename of the document to parse (e.g. 'invoice.pdf'). " +
              "Files in the VM's \\\\vboxsvr\\shared\\ folder can be referenced by filename alone.",
          },
        },
        required: ["file_path"],
      }),
      execute: async ({ file_path }: { file_path: string }) => {
        const markdown = await parseDocument(file_path);
        return { output: markdown };
      },
    } as any);
    systemPrompt += PARSE_TOOL_ADDENDUM;
  }

  console.log(`Loaded tools: ${Object.keys(tools).join(", ")}`);
  console.log(`Running with Claude: "${prompt}"\n`);

  const result = await generateText({
    model: anthropic("claude-sonnet-4-6") as any,
    tools,
    stopWhen: stepCountIs(100),
    system: systemPrompt,
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
