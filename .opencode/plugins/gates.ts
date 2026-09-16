import type { Plugin, PluginInput } from "@opencode-ai/plugin";

// Plugin core for stateful pipeline gates (specs/gates.md «Plugin-ядро», ADR-022/023).
// Only GatesPlugin is exported: the opencode loader treats every exported function
// as a plugin. Gate state lives in this process only — never shared between servers.

/** Hooks map returned by the plugin; source of hook/event types without importing the SDK directly. */
type PluginHooks = Awaited<ReturnType<Plugin>>;

/** Bus event delivered to the `event` hook. */
type BusEvent = Parameters<NonNullable<PluginHooks["event"]>>[0]["event"];

/** A tool invocation observed before execution. */
interface GateCall {
  tool: string;
  sessionID: string;
  callID: string;
  args: unknown;
}

/** A tool result observed after execution. */
interface GateResult extends GateCall {
  title: string;
  output: string;
  metadata: unknown;
}

/**
 * A stateful gate (specs/gates.md): observers run in registration order;
 * `before` may throw GateError to abort the tool call, other throws are a bug.
 */
interface Gate {
  id: string;
  before?(call: GateCall): void;
  after?(call: GateResult): void;
  onEvent?(event: BusEvent): void;
}

/** Intentional block: the message reaches the model as the tool error (specs/gates.md). */
class GateError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "GateError";
  }
}

/** Reports an unexpected gate failure to the server log; must never throw. */
type FailureLogger = (gateID: string, err: unknown) => Promise<void>;

/** Runs one gate observer: GateError propagates, any other error is logged and swallowed (fail-open). */
async function runGate(gate: Gate, logFailure: FailureLogger, invoke: () => void): Promise<void> {
  try {
    invoke();
  } catch (err) {
    if (err instanceof GateError) throw err;
    await logFailure(gate.id, err);
  }
}

/** Adapts a hook input plus the observed args into the gate-facing call shape. */
function gateCall(input: { tool: string; sessionID: string; callID: string }, args: unknown): GateCall {
  return { tool: input.tool, sessionID: input.sessionID, callID: input.callID, args };
}

/** Adapts a `tool.execute.after` input/output pair into the gate-facing result shape. */
function gateResult(
  input: { tool: string; sessionID: string; callID: string; args: unknown },
  output: { title: string; output: string; metadata: unknown },
): GateResult {
  return { ...gateCall(input, input.args), title: output.title, output: output.output, metadata: output.metadata };
}

/** Writes a gate bug to the server log via client.app.log; never throws (fail-open path). */
async function logGateFailure(client: PluginInput["client"], gateID: string, err: unknown): Promise<void> {
  const detail = err instanceof Error ? (err.stack ?? err.message) : String(err);
  try {
    await client.app.log({
      body: { service: "gates", level: "error", message: `gate ${gateID} failed: ${detail}`, extra: { gate: gateID } },
    });
  } catch {
    // Logging a gate bug must not break the tool call; there is no fallback sink.
  }
}

// ---------------------------------------------------------------------------
// Gate G7: green `make check` stamp required by `git commit` (ADR-023)
// ---------------------------------------------------------------------------

const BASH_TOOL = "bash";

// Tools whose invocation invalidates the stamp. opencode ≥1.18 registers the patcher as
// `apply_patch`; `patch` is kept for id compatibility across versions.
const MUTATING_TOOLS = new Set(["edit", "write", "patch", "apply_patch"]);

// git global options that consume the following token as their value.
const GIT_VALUE_FLAGS = new Set([
  "-C",
  "-c",
  "--exec-path",
  "--git-dir",
  "--work-tree",
  "--namespace",
  "--super-prefix",
]);

const ENV_PREFIX = /^[A-Za-z_][A-Za-z0-9_]*=.*/;

const COMMIT_INSTRUCTION =
  "Gate G7: 'git commit' is blocked — no green 'make check' stamp in this opencode process. " +
  "Run `make check`, confirm it exits 0, then repeat the commit; or use a self-guarded chain: " +
  '`make check && git commit -m "..."`. ' +
  "The stamp is set by a successful `make check` (bash) and invalidated by edit/write/patch edits.";

/** A command fragment between shell separators; sepBefore is the separator that ends it. */
interface Segment {
  text: string;
  sepBefore: string;
}

/** Splits a command line at shell separators `&& || ; |` and newlines, keeping each separator. */
function splitCommand(command: string): Segment[] {
  const separator = /\r?\n|&&|\|\||;|\|/g;
  const segments: Segment[] = [];
  let last = 0;
  let sepBefore = "";
  for (let m = separator.exec(command); m !== null; m = separator.exec(command)) {
    segments.push({ text: command.slice(last, m.index), sepBefore });
    sepBefore = m[0];
    last = m.index + m[0].length;
  }
  segments.push({ text: command.slice(last), sepBefore });
  return segments;
}

/** Splits a segment into whitespace-separated tokens (no quote lexing — matcher is conservative). */
function tokenize(segment: string): string[] {
  return segment.trim().split(/\s+/).filter((token) => token.length > 0);
}

/** Index of the first non `VAR=value` prefix token. */
function skipEnvPrefix(tokens: string[], from: number): number {
  let i = from;
  while (i < tokens.length && ENV_PREFIX.test(tokens[i])) i += 1;
  return i;
}

/** Segment invokes `make` with target `check`; `make` flags and `-C <dir>` are allowed. */
function isMakeCheckSegment(segment: string): boolean {
  const tokens = tokenize(segment);
  let i = skipEnvPrefix(tokens, 0);
  if (tokens[i] !== "make") return false;
  for (i += 1; i < tokens.length; i += 1) {
    const token = tokens[i];
    if (token === "-C" || token === "--directory") {
      i += 1; // consume the directory value
      continue;
    }
    if (token.startsWith("-")) continue;
    if (token === "check") return true;
  }
  return false;
}

/** Segment invokes `git` with subcommand `commit`; global flags and env prefixes are allowed. */
function isGitCommitSegment(segment: string): boolean {
  const tokens = tokenize(segment);
  let i = skipEnvPrefix(tokens, 0);
  if (tokens[i] !== "git") return false;
  for (i += 1; i < tokens.length; i += 1) {
    const token = tokens[i];
    if (GIT_VALUE_FLAGS.has(token)) {
      i += 1; // consume the option value
      continue;
    }
    if (token.startsWith("-")) continue;
    return token === "commit";
  }
  return false;
}

/** Extracts the bash `command` string from tool args; undefined for any other shape. */
function commandOf(args: unknown): string | undefined {
  if (typeof args !== "object" || args === null) return undefined;
  const command = (args as Record<string, unknown>).command;
  return typeof command === "string" ? command : undefined;
}

/** Positive exit-0 confirmation from bash result metadata (`metadata.exit` in opencode 1.18.x). */
function confirmedGreenExit(metadata: unknown): boolean {
  if (typeof metadata !== "object" || metadata === null) return false;
  const exit = (metadata as Record<string, unknown>).exit;
  return typeof exit === "number" && exit === 0;
}

/**
 * Whether a whole-line exit 0 confirms the check: true only when every segment after the last
 * matched `make check` is joined by `&&`; `; | ||` make the reported code belong to a later
 * command, so success would be unconfirmed (ADR-023: no stamp without confirmation).
 */
function stampsGreen(segments: Segment[]): boolean {
  const check = segments.findLastIndex((segment) => isMakeCheckSegment(segment.text));
  if (check === -1) return false;
  return segments.slice(check + 1).every((segment) => segment.sepBefore === "&&");
}

/** Blocks a `git commit` unless the stamp is green or an `&&`-linked earlier segment runs `make check`. */
function requireGreenStamp(command: string | undefined, green: boolean): void {
  if (green || command === undefined) return;
  const segments = splitCommand(command);
  const commit = segments.findIndex((segment) => isGitCommitSegment(segment.text));
  if (commit === -1) return;
  if (guardedByCheck(segments, commit)) return;
  throw new GateError(COMMIT_INSTRUCTION);
}

/**
 * Whether the commit segment sits at the end of an all-`&&` chain containing a `make check`:
 * on a red check the chain aborts before the commit, so no stamp is needed (specs/gates.md).
 */
function guardedByCheck(segments: Segment[], commit: number): boolean {
  let start = commit;
  while (start > 0 && segments[start].sepBefore === "&&") start -= 1;
  return segments.slice(start, commit).some((segment) => isMakeCheckSegment(segment.text));
}

/** G7: passive green stamp from `make check`, required fresh at `git commit`. */
function makeCheckBeforeCommitGate(): Gate {
  let green = false;
  return {
    id: "G7",
    before(call) {
      if (call.tool === BASH_TOOL) {
        requireGreenStamp(commandOf(call.args), green);
        return;
      }
      if (MUTATING_TOOLS.has(call.tool)) green = false;
    },
    after(call) {
      if (call.tool !== BASH_TOOL || !confirmedGreenExit(call.metadata)) return;
      const command = commandOf(call.args);
      if (command !== undefined && stampsGreen(splitCommand(command))) green = true;
    },
    onEvent(event) {
      if (event.type === "file.edited") green = false;
    },
  };
}

// ---------------------------------------------------------------------------
// Plugin core
// ---------------------------------------------------------------------------

/**
 * Gates plugin: registers one `tool.execute.before`, one `tool.execute.after`
 * and one `event` handler each, dispatching to the gate list in registration
 * order. A new gate is a new list entry — the core does not change.
 */
export const GatesPlugin: Plugin = async ({ client }) => {
  // Stamp state lives here for the whole server process, shared by all sessions (ADR-023).
  const gates: Gate[] = [makeCheckBeforeCommitGate()];
  const logFailure: FailureLogger = (gateID, err) => logGateFailure(client, gateID, err);

  return {
    "tool.execute.before": async (input, output) => {
      for (const gate of gates) {
        await runGate(gate, logFailure, () => gate.before?.(gateCall(input, output.args)));
      }
    },
    "tool.execute.after": async (input, output) => {
      for (const gate of gates) {
        await runGate(gate, logFailure, () => gate.after?.(gateResult(input, output)));
      }
    },
    event: async ({ event }) => {
      for (const gate of gates) {
        await runGate(gate, logFailure, () => gate.onEvent?.(event));
      }
    },
  };
};
