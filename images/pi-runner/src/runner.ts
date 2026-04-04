/**
 * pi-runner — Lightweight harness for running pi-agent-core agents as Kubernetes Jobs.
 *
 * This runner:
 * 1. Reads configuration from environment variables (set by the WorkflowRun controller)
 * 2. Dynamically imports the agent module from /agent/index.js
 * 3. Configures the model via @mariozechner/pi-ai
 * 4. Runs the agent with the provided prompt
 * 5. Streams all events as JSONL to stdout for log collection
 * 6. Writes the final result to /output/result.json (for ConfigMap pickup by the controller)
 * 7. Exits 0 on success, 1 on failure
 */

import { Agent } from "@mariozechner/pi-agent-core";
import type { AgentTool, AgentEvent, ThinkingLevel } from "@mariozechner/pi-agent-core";
import { getModel, streamSimple } from "@mariozechner/pi-ai";
import type { KnownProvider } from "@mariozechner/pi-ai";
import { writeFile, mkdir } from "node:fs/promises";
import { existsSync } from "node:fs";
import { dirname } from "node:path";

// =============================================================================
// ENVIRONMENT
// =============================================================================

interface RunnerConfig {
  modelProvider: string;
  modelName: string;
  providerApiKey: string;
  thinkingLevel: ThinkingLevel | "off";
  toolExecution: "parallel" | "sequential";
  prompt: string;
  triggerData: string;
  agentModulePath: string;
  outputDir: string;
}

function loadConfig(): RunnerConfig {
  const model = requireEnv("MODEL_PROVIDER");
  const modelName = requireEnv("MODEL_NAME");
  const apiKey = requireEnv("PROVIDER_API_KEY");

  return {
    modelProvider: model,
    modelName: modelName,
    providerApiKey: apiKey,
    thinkingLevel: (process.env["THINKING_LEVEL"] ?? "off") as ThinkingLevel | "off",
    toolExecution: (process.env["TOOL_EXECUTION"] ?? "parallel") as "parallel" | "sequential",
    prompt: requireEnv("PROMPT"),
    triggerData: process.env["TRIGGER_DATA"] ?? "{}",
    agentModulePath: process.env["AGENT_MODULE_PATH"] ?? "/agent/index.js",
    outputDir: process.env["OUTPUT_DIR"] ?? "/output",
  };
}

function requireEnv(name: string): string {
  const value = process.env[name];
  if (!value) {
    fatal(`Required environment variable ${name} is not set`);
  }
  return value;
}

// =============================================================================
// JSONL EVENT STREAMING
// =============================================================================

/**
 * Emit a JSONL event line to stdout.
 * Each line is a self-contained JSON object with type, timestamp, and optional data.
 */
function emit(type: string, data?: Record<string, unknown>): void {
  const event: Record<string, unknown> = {
    type,
    ts: Date.now(),
  };
  if (data !== undefined) {
    event.data = data;
  }
  // Use process.stdout.write to avoid extra newlines from console.log
  process.stdout.write(JSON.stringify(event) + "\n");
}

/**
 * Log a message to stderr (not part of the JSONL event stream).
 */
function log(message: string): void {
  process.stderr.write(`[pi-runner] ${message}\n`);
}

/**
 * Log a fatal error and exit.
 */
function fatal(message: string): never {
  emit("error", { message });
  process.stderr.write(`[pi-runner] FATAL: ${message}\n`);
  process.exit(1);
}

// =============================================================================
// AGENT MODULE LOADING
// =============================================================================

interface AgentModule {
  tools?: AgentTool[];
  config?: {
    systemPrompt?: string;
  };
}

async function loadAgentModule(modulePath: string): Promise<AgentModule> {
  // Handle inline code: if AGENT_INLINE_CODE is set, write it to the module path
  const inlineCode = process.env["AGENT_INLINE_CODE"];
  if (inlineCode) {
    log("Writing inline agent code to disk");
    const dir = dirname(modulePath);
    if (!existsSync(dir)) {
      await mkdir(dir, { recursive: true });
    }
    await writeFile(modulePath, inlineCode, "utf-8");
    log(`Inline code written to ${modulePath} (${inlineCode.length} chars)`);
  }

  if (!existsSync(modulePath)) {
    fatal(`Agent module not found at ${modulePath}`);
  }

  log(`Loading agent module from ${modulePath}`);

  try {
    const mod = await import(modulePath);
    const agentModule: AgentModule = {
      tools: mod.tools ?? mod.default?.tools ?? [],
      config: mod.config ?? mod.default?.config ?? {},
    };

    log(`Loaded ${agentModule.tools?.length ?? 0} tools`);
    return agentModule;
  } catch (err) {
    fatal(`Failed to load agent module: ${err instanceof Error ? err.message : String(err)}`);
  }
}

// =============================================================================
// RESULT COLLECTION
// =============================================================================

interface RunResult {
  success: boolean;
  messages: string[];
  toolCalls: Array<{ name: string; args: unknown; result: unknown }>;
  tokensUsed: number;
  error?: string;
}

async function writeResult(outputDir: string, result: RunResult): Promise<void> {
  try {
    if (!existsSync(outputDir)) {
      await mkdir(outputDir, { recursive: true });
    }
    const resultPath = `${outputDir}/result.json`;
    await writeFile(resultPath, JSON.stringify(result, null, 2), "utf-8");
    log(`Result written to ${resultPath}`);
  } catch (err) {
    // Non-fatal — the event stream is the primary output
    log(`Warning: failed to write result file: ${err instanceof Error ? err.message : String(err)}`);
  }
}

// =============================================================================
// MAIN
// =============================================================================

async function main(): Promise<void> {
  log("Starting pi-runner");
  emit("runner_start");

  // 1. Load configuration
  const config = loadConfig();
  log(`Model: ${config.modelProvider}/${config.modelName}`);
  log(`Thinking: ${config.thinkingLevel}, Tool execution: ${config.toolExecution}`);

  // 2. Load agent module
  const agentModule = await loadAgentModule(config.agentModulePath);

  // 3. Configure the model
  // Set the API key in the environment where the provider expects it.
  // pi-ai's getModel/streamSimple reads standard env vars (ANTHROPIC_API_KEY, OPENAI_API_KEY, etc.)
  const envKeyMap: Record<string, string> = {
    anthropic: "ANTHROPIC_API_KEY",
    openai: "OPENAI_API_KEY",
    google: "GOOGLE_API_KEY",
    "google-vertex": "GOOGLE_API_KEY",
    mistral: "MISTRAL_API_KEY",
    xai: "XAI_API_KEY",
    groq: "GROQ_API_KEY",
    cerebras: "CEREBRAS_API_KEY",
    openrouter: "OPENROUTER_API_KEY",
    "amazon-bedrock": "AWS_SECRET_ACCESS_KEY",
  };

  const envKey = envKeyMap[config.modelProvider.toLowerCase()];
  if (envKey) {
    process.env[envKey] = config.providerApiKey;
  } else {
    // For unknown providers, set a generic key — pi-ai may still resolve it
    log(`Unknown provider "${config.modelProvider}", setting generic PROVIDER_API_KEY`);
  }

  // getModel() expects KnownProvider literal types at compile time.
  // At runtime, the provider and model strings come from env vars.
  // We use the untyped form since the exact provider/model pair is only known at runtime.
  const model = (getModel as (provider: string, modelId: string) => ReturnType<typeof getModel>)(
    config.modelProvider,
    config.modelName,
  );

  // 4. Build system prompt
  let systemPrompt = agentModule.config?.systemPrompt ?? "";

  // Inject trigger data into the system prompt context
  if (config.triggerData && config.triggerData !== "{}") {
    systemPrompt += `\n\nTrigger data (context from the workflow trigger):\n${config.triggerData}`;
  }

  // 5. Create and configure the agent
  // Agent uses initialState for model/tools/systemPrompt/thinkingLevel,
  // and streamFn for the LLM streaming backend.
  const thinkingLevel: ThinkingLevel = config.thinkingLevel === "off"
    ? "minimal"  // "off" is not in pi-ai's ThinkingLevel type; use "minimal" as the floor
    : config.thinkingLevel;

  const agent = new Agent({
    initialState: {
      model,
      tools: agentModule.tools ?? [],
      systemPrompt,
      thinkingLevel,
    },
    streamFn: streamSimple,
    getApiKey: async (provider: string) => {
      // Return the API key for any provider — we set it up in env above,
      // but also provide it directly for safety.
      return config.providerApiKey;
    },
    toolExecution: config.toolExecution,
  });

  // 6. Subscribe to events and stream as JSONL
  const result: RunResult = {
    success: false,
    messages: [],
    toolCalls: [],
    tokensUsed: 0,
  };

  agent.subscribe((event: AgentEvent, _signal: AbortSignal) => {
    // Forward all events as JSONL
    emit(event.type, eventToData(event));

    // Collect results for the output file
    switch (event.type) {
      case "message_end": {
        // Extract text content from the agent message
        const msg = event.message;
        if (msg && "content" in msg) {
          const content = msg.content;
          if (typeof content === "string") {
            result.messages.push(content);
          } else if (Array.isArray(content)) {
            const text = content
              .filter((c): c is { type: "text"; text: string } => typeof c === "object" && c !== null && "type" in c && c.type === "text")
              .map((c) => c.text)
              .join("");
            if (text) result.messages.push(text);
          }
        }
        break;
      }
      case "tool_execution_end":
        result.toolCalls.push({
          name: event.toolName,
          args: undefined, // args not available in tool_execution_end
          result: event.result,
        });
        break;
      case "agent_end": {
        // Sum up token usage from all assistant messages
        const msgs = event.messages ?? [];
        for (const m of msgs) {
          if (m && "usage" in m && typeof m.usage === "object" && m.usage !== null) {
            result.tokensUsed += (m.usage as { totalTokens?: number }).totalTokens ?? 0;
          }
        }
        break;
      }
    }
  });

  // 7. Run the agent
  log(`Executing prompt (${config.prompt.length} chars)`);

  try {
    await agent.prompt(config.prompt);
    // Wait for all event listeners to settle
    await agent.waitForIdle();
    result.success = true;
    log("Agent execution completed successfully");
  } catch (err) {
    const errorMessage = err instanceof Error ? err.message : String(err);
    result.success = false;
    result.error = errorMessage;
    emit("error", { message: errorMessage });
    log(`Agent execution failed: ${errorMessage}`);
  }

  // 8. Write result file
  await writeResult(config.outputDir, result);

  // 9. Exit
  if (!result.success) {
    process.exit(1);
  }

  log("pi-runner finished");
}

/**
 * Convert an AgentEvent to a plain data object for JSONL serialization.
 * Strips the `type` field (already emitted as the event type) and serializes
 * message objects safely.
 */
function eventToData(event: AgentEvent): Record<string, unknown> {
  const data: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(event)) {
    if (key === "type") continue;
    data[key] = value;
  }
  return data;
}

// Run
main().catch((err) => {
  fatal(`Unhandled error: ${err instanceof Error ? err.message : String(err)}`);
});
