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
import { writeFile, mkdir, readdir } from "node:fs/promises";
import { existsSync } from "node:fs";
import { dirname, join } from "node:path";

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
// CALLBACK CLIENT
// =============================================================================

/**
 * Callback client for sending real-time events to the operator.
 * If CALLBACK_URL is set, events are POSTed to the operator's callback endpoint
 * in a fire-and-forget manner (non-blocking, failures are logged but don't crash).
 */
const callbackUrl = process.env["CALLBACK_URL"];

/**
 * POST a StepEvent to the operator callback endpoint.
 * Fire-and-forget: does not block, logs errors to stderr.
 */
function sendCallback(event: {
  type: string;
  ts: number;
  toolName?: string;
  toolArgs?: string;
  toolResult?: string;
  duration?: number;
  content?: string;
}): void {
  if (!callbackUrl) return;

  // Fire-and-forget — use .catch to suppress unhandled rejection
  fetch(callbackUrl, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(event),
    signal: AbortSignal.timeout(5000), // 5s timeout per event
  }).catch((err) => {
    log(`Callback failed: ${err instanceof Error ? err.message : String(err)}`);
  });
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
// TOOL REF LOADING
// =============================================================================

/**
 * Scan /tools/<name>/index.js for tool packages pulled by init containers.
 * Each directory should contain an index.js that exports a `tools` array of AgentTool[].
 * Returns the merged array of all discovered tools.
 */
async function loadToolRefs(toolsDir: string = "/tools"): Promise<AgentTool[]> {
  const allTools: AgentTool[] = [];

  if (!existsSync(toolsDir)) {
    log("No /tools directory found — no toolRefs configured");
    return allTools;
  }

  let entries;
  try {
    entries = await readdir(toolsDir, { withFileTypes: true });
  } catch (err) {
    log(`Warning: failed to read tools directory: ${err instanceof Error ? err.message : String(err)}`);
    return allTools;
  }

  for (const entry of entries) {
    if (!entry.isDirectory()) continue;

    const indexPath = join(toolsDir, entry.name, "index.js");
    if (!existsSync(indexPath)) {
      log(`Warning: tool package "${entry.name}" has no index.js — skipping`);
      continue;
    }

    try {
      const mod = await import(indexPath);
      const tools = mod.tools ?? mod.default?.tools;
      if (Array.isArray(tools) && tools.length > 0) {
        allTools.push(...tools);
        log(`Loaded ${tools.length} tools from toolRef "${entry.name}"`);
      } else {
        log(`Warning: tool package "${entry.name}" exports no tools array`);
      }
    } catch (err) {
      log(`Warning: failed to load tools from "${entry.name}": ${err instanceof Error ? err.message : String(err)}`);
    }
  }

  return allTools;
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
// GIT AUTH
// =============================================================================

/**
 * Configure git HTTPS authentication using GIT_ASKPASS.
 *
 * Mirrors the ConfigureGitAuth() logic from capability-gateway (gateway.go).
 * Checks for GITLAB_TOKEN, GH_TOKEN, or GITHUB_TOKEN env vars and creates
 * a shell script that echoes the appropriate token when git asks for a password.
 */
async function configureGitAuth(): Promise<void> {
  const glToken = process.env["GITLAB_TOKEN"];
  const ghToken = process.env["GH_TOKEN"] || process.env["GITHUB_TOKEN"];

  if (!glToken && !ghToken) {
    log("No git tokens found (GITLAB_TOKEN, GH_TOKEN, GITHUB_TOKEN) — git auth not configured");
    return;
  }

  // Build an askpass script that returns the right token based on the host
  const lines = ["#!/bin/sh"];
  if (glToken) {
    lines.push(`echo "${glToken}"`);
  } else if (ghToken) {
    lines.push(`echo "${ghToken}"`);
  }

  const askpassPath = "/tmp/git-askpass.sh";
  await writeFile(askpassPath, lines.join("\n") + "\n", { mode: 0o755 });

  process.env["GIT_ASKPASS"] = askpassPath;
  process.env["GIT_TERMINAL_PROMPT"] = "0";

  // Configure git to use the token as username for HTTPS clones
  // This makes `git clone https://gitlab.com/...` work without user interaction
  if (glToken) {
    // For GitLab, configure the credential helper inline via git config env vars
    // Also set the URL to include oauth2 prefix so git uses the token as password
    const gitlabUrl = process.env["GITLAB_URL"] || "https://gitlab.com";
    const host = new URL(gitlabUrl).host;
    process.env["GIT_CONFIG_COUNT"] = "1";
    process.env["GIT_CONFIG_KEY_0"] = `url.https://oauth2:${glToken}@${host}/.insteadOf`;
    process.env["GIT_CONFIG_VALUE_0"] = `https://${host}/`;
    log(`Git auth configured for GitLab (${host}) via URL rewrite + askpass`);
  } else if (ghToken) {
    process.env["GIT_CONFIG_COUNT"] = "1";
    process.env["GIT_CONFIG_KEY_0"] = `url.https://x-access-token:${ghToken}@github.com/.insteadOf`;
    process.env["GIT_CONFIG_VALUE_0"] = "https://github.com/";
    log("Git auth configured for GitHub via URL rewrite + askpass");
  }
}

// =============================================================================
// MAIN
// =============================================================================

async function main(): Promise<void> {
  log("Starting pi-runner");
  emit("runner_start");

  // Log callback configuration
  if (callbackUrl) {
    log(`Callback URL: ${callbackUrl}`);
  } else {
    log("No CALLBACK_URL set — real-time tracing disabled");
  }

  // 0. Configure git HTTPS auth (before anything that might use git)
  await configureGitAuth();

  // 1. Load configuration
  const config = loadConfig();
  log(`Model: ${config.modelProvider}/${config.modelName}`);
  log(`Thinking: ${config.thinkingLevel}, Tool execution: ${config.toolExecution}`);

  // 2. Load agent module
  const agentModule = await loadAgentModule(config.agentModulePath);

  // 3. Load tool refs (OCI tool packages pulled by init containers into /tools/<name>/)
  const toolRefTools = await loadToolRefs();
  const allTools = [...(agentModule.tools ?? []), ...toolRefTools];
  log(`Total tools: ${allTools.length} (${agentModule.tools?.length ?? 0} from agent, ${toolRefTools.length} from toolRefs)`);

  // 4. Configure the model
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

  // 5. Build system prompt
  let systemPrompt = agentModule.config?.systemPrompt ?? "";

  // Inject trigger data into the system prompt context
  if (config.triggerData && config.triggerData !== "{}") {
    systemPrompt += `\n\nTrigger data (context from the workflow trigger):\n${config.triggerData}`;
  }

  // 6. Create and configure the agent
  // Agent uses initialState for model/tools/systemPrompt/thinkingLevel,
  // and streamFn for the LLM streaming backend.
  const thinkingLevel: ThinkingLevel = config.thinkingLevel === "off"
    ? "minimal"  // "off" is not in pi-ai's ThinkingLevel type; use "minimal" as the floor
    : config.thinkingLevel;

  const agent = new Agent({
    initialState: {
      model,
      tools: allTools,
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

  // 7. Subscribe to events and stream as JSONL
  const result: RunResult = {
    success: false,
    messages: [],
    toolCalls: [],
    tokensUsed: 0,
  };

  agent.subscribe((event: AgentEvent, _signal: AbortSignal) => {
    // Forward all events as JSONL
    emit(event.type, eventToData(event));

    // Collect results for the output file + send callback events
    switch (event.type) {
      case "message_end": {
        // Extract text content from the agent message
        const msg = event.message;
        let extractedText = "";
        if (msg && "content" in msg) {
          const content = msg.content;
          if (typeof content === "string") {
            extractedText = content;
          } else if (Array.isArray(content)) {
            extractedText = content
              .filter((c): c is { type: "text"; text: string } => typeof c === "object" && c !== null && "type" in c && c.type === "text")
              .map((c) => c.text)
              .join("");
          }
        }
        if (extractedText) {
          result.messages.push(extractedText);
          sendCallback({
            type: "message",
            ts: Date.now(),
            content: extractedText,
          });
        }
        break;
      }
      case "tool_execution_end": {
        result.toolCalls.push({
          name: event.toolName,
          args: undefined, // args not available in tool_execution_end
          result: event.result,
        });
        // Send tool call callback with result serialized as JSON string
        let toolResultStr = "";
        try {
          toolResultStr = typeof event.result === "string" ? event.result : JSON.stringify(event.result);
        } catch { toolResultStr = String(event.result); }

        sendCallback({
          type: "tool_call",
          ts: Date.now(),
          toolName: event.toolName,
          toolResult: toolResultStr,
        });
        break;
      }
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

  // 8. Run the agent
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
    sendCallback({
      type: "error",
      ts: Date.now(),
      content: errorMessage,
    });
    log(`Agent execution failed: ${errorMessage}`);
  }

  // 9. Write result file
  await writeResult(config.outputDir, result);

  // 10. Exit
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
