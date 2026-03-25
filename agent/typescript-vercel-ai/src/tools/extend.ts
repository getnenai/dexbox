import { parseDocument as sharedParse } from "../../../shared/extend-parse.js";
import type { ExtendParseResult } from "../../../shared/extend-parse.js";

export type { ExtendParseResult };

export async function parseDocument(filePath: string): Promise<string> {
  const result = await sharedParse(filePath);
  return result.markdown;
}

export async function parseDocumentFull(filePath: string): Promise<ExtendParseResult> {
  return sharedParse(filePath);
}
