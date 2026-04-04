/**
 * tool-bridge — MCP stdio server that bridges AgentTool[] packages to the MCP protocol.
 *
 * This enables a single set of tool packages (e.g., tools/git, tools/gitlab, tools/file)
 * to be shared between:
 *   - PiAgent (direct JS import via pi-runner)
 *   - OpenCode agents (via this MCP bridge + capability-gateway SSE proxy)
 *
 * Architecture:
 *   1. On startup, scans /tools/<name>/index.js for tool packages
 *   2. Imports each package and collects all AgentTool[] definitions
 *   3. Implements the MCP protocol (JSON-RPC 2.0 over stdio):
 *      - initialize → returns server info + capabilities
 *      - tools/list → returns all discovered tools as MCP tool definitions
 *      - tools/call → invokes the tool's execute() function
 *   4. The capability-gateway spawns this process and bridges stdio to SSE
 *
 * Environment variables:
 *   TOOLS_DIR      - Directory to scan for tool packages (default: /tools)
 *   WORKSPACE      - Working directory for tools (default: /workspace)
 *   SERVER_NAME    - MCP server name (default: tool-bridge)
 *   SERVER_VERSION - MCP server version (default: 0.1.0)
 *   LOG_LEVEL      - debug, info, warn, error (default: info)
 *
 * @module tool-bridge
 */

import { createInterface } from "node:readline";
import { readdir } from "node:fs/promises";
import { existsSync } from "node:fs";
import { join } from "node:path";

// =============================================================================
// TYPES
// =============================================================================

/**
 * AgentTool interface — matches the contract used by tool packages in agent-tools.
 * This is the shared interface between pi-runner and tool-bridge.
 */
interface AgentTool {
  name: string;
  description: string;
  label?: string;
  parameters: {
    type: string;
    properties?: Record<string, unknown>;
    required?: string[];
  };
  execute: (id: string, params: Record<string, unknown>) => Promise<AgentToolResult>;
}

interface AgentToolResult {
  content: Array<{ type: string; text?: string; data?: string; mimeType?: string }>;
  details?: Record<string, unknown>;
}

/** JSON-RPC 2.0 request */
interface JsonRpcRequest {
  jsonrpc: "2.0";
  id?: string | number | null;
  method: string;
  params?: Record<string, unknown>;
}

/** JSON-RPC 2.0 response */
interface JsonRpcResponse {
  jsonrpc: "2.0";
  id: string | number | null;
  result?: unknown;
  error?: { code: number; message: string; data?: unknown };
}

// =============================================================================
// LOGGING
// =============================================================================

const LOG_LEVELS: Record<string, number> = { debug: 0, info: 1, warn: 2, error: 3 };
const currentLogLevel = LOG_LEVELS[process.env["LOG_LEVEL"] ?? "info"] ?? 1;

function log(level: string, message: string, data?: Record<string, unknown>): void {
  if ((LOG_LEVELS[level] ?? 1) < currentLogLevel) return;
  const entry: Record<string, unknown> = {
    level,
    ts: new Date().toISOString(),
    msg: message,
  };
  if (data) Object.assign(entry, data);
  // Log to stderr — stdout is reserved for MCP JSON-RPC messages
  process.stderr.write(JSON.stringify(entry) + "\n");
}

// =============================================================================
// TOOL PACKAGE LOADER
// =============================================================================

/**
 * Scan a directory for tool packages and load all AgentTool[] definitions.
 *
 * Each subdirectory should contain an index.js that exports a `tools` array.
 * This is the same loading pattern used by pi-runner's loadToolRefs().
 */
async function loadToolPackages(toolsDir: string): Promise<Map<string, AgentTool>> {
  const tools = new Map<string, AgentTool>();

  if (!existsSync(toolsDir)) {
    log("warn", "Tools directory not found", { path: toolsDir });
    return tools;
  }

  let entries;
  try {
    entries = await readdir(toolsDir, { withFileTypes: true });
  } catch (err) {
    log("error", "Failed to read tools directory", {
      path: toolsDir,
      error: err instanceof Error ? err.message : String(err),
    });
    return tools;
  }

  for (const entry of entries) {
    if (!entry.isDirectory()) continue;

    const indexPath = join(toolsDir, entry.name, "index.js");
    if (!existsSync(indexPath)) {
      log("warn", `Tool package "${entry.name}" has no index.js — skipping`);
      continue;
    }

    try {
      const mod = await import(indexPath);
      const toolArray: AgentTool[] = mod.tools ?? mod.default?.tools;

      if (!Array.isArray(toolArray) || toolArray.length === 0) {
        log("warn", `Tool package "${entry.name}" exports no tools array`);
        continue;
      }

      for (const tool of toolArray) {
        if (tools.has(tool.name)) {
          log("warn", `Duplicate tool name "${tool.name}" from package "${entry.name}" — overwriting`);
        }
        tools.set(tool.name, tool);
      }

      log("info", `Loaded ${toolArray.length} tools from package "${entry.name}"`, {
        tools: toolArray.map((t) => t.name),
      });
    } catch (err) {
      log("error", `Failed to load tool package "${entry.name}"`, {
        error: err instanceof Error ? err.message : String(err),
      });
    }
  }

  return tools;
}

// =============================================================================
// MCP PROTOCOL HANDLERS
// =============================================================================

const SERVER_NAME = process.env["SERVER_NAME"] ?? "tool-bridge";
const SERVER_VERSION = process.env["SERVER_VERSION"] ?? "0.1.0";

function handleInitialize(req: JsonRpcRequest): JsonRpcResponse {
  return {
    jsonrpc: "2.0",
    id: req.id ?? null,
    result: {
      protocolVersion: "2024-11-05",
      capabilities: {
        tools: {
          listChanged: false,
        },
      },
      serverInfo: {
        name: SERVER_NAME,
        version: SERVER_VERSION,
      },
    },
  };
}

function handleToolsList(req: JsonRpcRequest, tools: Map<string, AgentTool>): JsonRpcResponse {
  const mcpTools = Array.from(tools.values()).map((tool) => ({
    name: tool.name,
    description: tool.description,
    inputSchema: tool.parameters,
  }));

  return {
    jsonrpc: "2.0",
    id: req.id ?? null,
    result: { tools: mcpTools },
  };
}

async function handleToolsCall(
  req: JsonRpcRequest,
  tools: Map<string, AgentTool>,
): Promise<JsonRpcResponse> {
  const params = req.params as { name?: string; arguments?: Record<string, unknown> } | undefined;

  if (!params?.name) {
    return {
      jsonrpc: "2.0",
      id: req.id ?? null,
      error: { code: -32602, message: "Missing required parameter: name" },
    };
  }

  const tool = tools.get(params.name);
  if (!tool) {
    return {
      jsonrpc: "2.0",
      id: req.id ?? null,
      error: { code: -32602, message: `Unknown tool: ${params.name}` },
    };
  }

  log("info", `Executing tool: ${params.name}`, { arguments: params.arguments });

  try {
    // Generate a unique call ID for the tool
    const callId = `${params.name}-${Date.now()}`;
    const result = await tool.execute(callId, params.arguments ?? {});

    // Convert AgentToolResult to MCP content format
    const content = result.content.map((item) => {
      if (item.type === "text") {
        return { type: "text" as const, text: item.text ?? "" };
      }
      if (item.type === "image" && item.data) {
        return {
          type: "image" as const,
          data: item.data,
          mimeType: item.mimeType ?? "image/png",
        };
      }
      // Default: treat as text
      return { type: "text" as const, text: item.text ?? JSON.stringify(item) };
    });

    log("info", `Tool ${params.name} completed successfully`);

    return {
      jsonrpc: "2.0",
      id: req.id ?? null,
      result: { content, isError: false },
    };
  } catch (err) {
    const errorMessage = err instanceof Error ? err.message : String(err);
    log("error", `Tool ${params.name} failed`, { error: errorMessage });

    return {
      jsonrpc: "2.0",
      id: req.id ?? null,
      result: {
        content: [{ type: "text", text: `Error: ${errorMessage}` }],
        isError: true,
      },
    };
  }
}

// =============================================================================
// MCP STDIO TRANSPORT
// =============================================================================

/**
 * Process a single JSON-RPC request and return a response.
 * Notifications (no id) return null — they don't expect a response.
 */
async function processRequest(
  req: JsonRpcRequest,
  tools: Map<string, AgentTool>,
  initialized: { value: boolean },
): Promise<JsonRpcResponse | null> {
  log("debug", `Received: ${req.method}`, { id: req.id });

  // Handle notifications (no response expected)
  if (req.method === "notifications/initialized") {
    initialized.value = true;
    log("info", "Client initialized");
    return null;
  }

  // All other notifications — no response
  if (req.method.startsWith("notifications/") && req.id === undefined) {
    return null;
  }

  // Handle requests
  switch (req.method) {
    case "initialize":
      return handleInitialize(req);

    case "tools/list":
      return handleToolsList(req, tools);

    case "tools/call":
      return await handleToolsCall(req, tools);

    case "ping":
      return { jsonrpc: "2.0", id: req.id ?? null, result: {} };

    default:
      return {
        jsonrpc: "2.0",
        id: req.id ?? null,
        error: { code: -32601, message: `Method not found: ${req.method}` },
      };
  }
}

/**
 * Send a JSON-RPC response to stdout.
 * MCP stdio transport uses newline-delimited JSON.
 */
function sendResponse(response: JsonRpcResponse): void {
  process.stdout.write(JSON.stringify(response) + "\n");
}

// =============================================================================
// MAIN
// =============================================================================

async function main(): Promise<void> {
  const toolsDir = process.env["TOOLS_DIR"] ?? "/tools";

  log("info", "Starting tool-bridge MCP server", {
    toolsDir,
    serverName: SERVER_NAME,
    serverVersion: SERVER_VERSION,
  });

  // Load all tool packages
  const tools = await loadToolPackages(toolsDir);
  log("info", `Loaded ${tools.size} tools total`, {
    tools: Array.from(tools.keys()),
  });

  if (tools.size === 0) {
    log("warn", "No tools loaded — the MCP server will report an empty tool list");
  }

  // Set up stdio transport
  const rl = createInterface({
    input: process.stdin,
    terminal: false,
  });

  const initialized = { value: false };

  rl.on("line", async (line: string) => {
    const trimmed = line.trim();
    if (!trimmed) return;

    let req: JsonRpcRequest;
    try {
      req = JSON.parse(trimmed) as JsonRpcRequest;
    } catch {
      // Invalid JSON — send parse error
      sendResponse({
        jsonrpc: "2.0",
        id: null,
        error: { code: -32700, message: "Parse error" },
      });
      return;
    }

    try {
      const response = await processRequest(req, tools, initialized);
      if (response !== null) {
        sendResponse(response);
      }
    } catch (err) {
      // Internal error — should not happen, but safety net
      log("error", "Internal error processing request", {
        method: req.method,
        error: err instanceof Error ? err.message : String(err),
      });
      sendResponse({
        jsonrpc: "2.0",
        id: req.id ?? null,
        error: {
          code: -32603,
          message: `Internal error: ${err instanceof Error ? err.message : String(err)}`,
        },
      });
    }
  });

  rl.on("close", () => {
    log("info", "stdin closed — shutting down");
    process.exit(0);
  });

  // Handle signals gracefully
  process.on("SIGINT", () => {
    log("info", "Received SIGINT — shutting down");
    process.exit(0);
  });
  process.on("SIGTERM", () => {
    log("info", "Received SIGTERM — shutting down");
    process.exit(0);
  });

  log("info", "MCP stdio transport ready — waiting for input");
}

main().catch((err) => {
  log("error", "Fatal error", { error: err instanceof Error ? err.message : String(err) });
  process.exit(1);
});
