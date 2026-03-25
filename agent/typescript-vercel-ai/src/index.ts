import { config } from "dotenv";
import { resolve, dirname } from "path";
import { fileURLToPath } from "url";
import { createInterface } from "readline";

// Load .env from the agent/ directory (two levels up from src/)
const __dirname = dirname(fileURLToPath(import.meta.url));
config({ path: resolve(__dirname, "..", "..", ".env") });
import { runClaude } from "./runners/claude.js";
import { runLux } from "./runners/lux.js";

const LUX_MODELS = ["lux-actor-1", "lux-thinker-1"] as const;
type LuxModel = (typeof LUX_MODELS)[number];

function isLuxModel(m: string): m is LuxModel {
  return LUX_MODELS.includes(m as LuxModel);
}

function parseArgs(): { model: string; prompt: string } {
  const args = process.argv.slice(2);
  let model = "claude";

  const modelIdx = args.indexOf("--model");
  if (modelIdx !== -1) {
    const modelValue = args[modelIdx + 1];
    if (!modelValue || modelValue.startsWith("--")) {
      console.error("Missing value for --model");
      process.exit(1);
    }
    model = modelValue;
    args.splice(modelIdx, 2);
  }

  const prompt = args.join(" ");
  return { model, prompt };
}

async function runPrompt(model: string, prompt: string): Promise<void> {
  let result: string;
  if (model === "claude") {
    result = await runClaude(prompt);
  } else {
    result = await runLux(prompt, model as LuxModel);
  }

  console.log("\n--- Result ---");
  console.log(result);
}

async function main() {
  const { model, prompt } = parseArgs();

  // Validate model
  if (model !== "claude" && !isLuxModel(model)) {
    console.error(
      `Unknown model: ${model}. Use: claude, ${LUX_MODELS.join(", ")}`
    );
    process.exit(1);
  }

  // Validate API keys
  if (model === "claude" && !process.env.ANTHROPIC_API_KEY) {
    console.error(
      "ANTHROPIC_API_KEY is not set. Create a .env file from .env.example."
    );
    process.exit(1);
  }
  if (isLuxModel(model) && !process.env.OAGI_API_KEY) {
    console.error("OAGI_API_KEY is not set. Add it to your .env file.");
    process.exit(1);
  }

  // Check dexbox connectivity
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

  // If a prompt was passed on the command line, run it as a one-shot
  if (prompt) {
    console.log(`Running with ${model}: "${prompt}"`);
    await runPrompt(model, prompt);
    return;
  }

  // Interactive REPL mode
  console.log(`dexbox Vercel AI Agent`);
  console.log(`   Model: ${model}`);
  console.log();
  console.log("Type your instruction (or 'quit' to exit):");
  console.log();

  const rl = createInterface({
    input: process.stdin,
    output: process.stdout,
  });

  const askQuestion = (): void => {
    rl.question("> ", async (line) => {
      const input = line.trim();
      if (!input) {
        askQuestion();
        return;
      }
      if (["quit", "exit", "q"].includes(input.toLowerCase())) {
        console.log("Bye!");
        rl.close();
        return;
      }

      try {
        await runPrompt(model, input);
      } catch (err) {
        console.error(err);
      }
      console.log();
      askQuestion();
    });
  };

  rl.on("close", () => process.exit(0));
  askQuestion();
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
