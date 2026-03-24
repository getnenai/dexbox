import "dotenv/config";
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

async function main() {
  const { model, prompt } = parseArgs();

  if (!prompt) {
    console.error(
      "Usage: npx tsx src/index.ts [--model claude|lux-actor-1|lux-thinker-1] <prompt>"
    );
    process.exit(1);
  }

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

  let result: string;
  if (model === "claude") {
    result = await runClaude(prompt);
  } else {
    result = await runLux(prompt, model as LuxModel);
  }

  console.log("\n--- Result ---");
  console.log(result);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
