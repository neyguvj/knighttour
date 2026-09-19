import { existsSync } from "node:fs";
import { dirname, resolve } from "node:path";
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
// Gate G8: main tree read-only while a feature worktree is active (ADR-024)
// ---------------------------------------------------------------------------

// Tools taking a single `filePath` target; patch tools carry paths inside their text body.
const EDIT_TOOLS = new Set(["edit", "write"]);
const PATCH_TOOLS = new Set(["patch", "apply_patch"]);

// Path lines of the patch format: `*** Update File: <path>` (Add/Delete alike).
const PATCH_FILE_LINE = /^\*\*\* (?:Update|Add|Delete) File: (.+)$/gm;

/** Builds the G8 block instruction naming the worktrees that pin the session. */
function mainEditInstruction(active: Set<string>): string {
  return (
    "Gate G8: editing the main tree is blocked while a feature worktree is active " +
    `(${[...active].join(", ")}). Apply every change inside the active worktree (absolute paths, ` +
    'bash with cwd set to it); if main must really change, close/remove the feature worktrees first ' +
    "(`git worktree remove …`). ADR-024 WORKTREE contract."
  );
}

/** Extracts a string field from tool args; undefined for any other shape. */
function stringField(args: unknown, field: string): string | undefined {
  if (typeof args !== "object" || args === null) return undefined;
  const value = (args as Record<string, unknown>)[field];
  return typeof value === "string" ? value : undefined;
}

/** Removes one layer of matching quotes from a token (tokenize does no quote lexing). */
function stripQuotes(token: string): string {
  return token.replace(/^["']|["']$/g, "");
}

/** A `git worktree add|remove <path>` invocation; `base` is the `-C <dir>` when present. */
interface WorktreeOp {
  sub: "add" | "remove";
  path: string | undefined;
  base: string | undefined;
}

/** Parses bash command segments for `git … worktree add|remove <path>` invocations (token matcher). */
function worktreeOps(command: string): WorktreeOp[] {
  const ops: WorktreeOp[] = [];
  for (const segment of splitCommand(command)) {
    const tokens = tokenize(segment.text);
    let i = skipEnvPrefix(tokens, 0);
    if (tokens[i] !== "git") continue;
    let base: string | undefined;
    i += 1;
    for (; i < tokens.length && tokens[i] !== "worktree"; i += 1) {
      const token = tokens[i];
      if (GIT_VALUE_FLAGS.has(token)) {
        if (token === "-C" && tokens[i + 1] !== undefined) base = stripQuotes(tokens[i + 1]);
        i += 1; // consume the option value
        continue;
      }
      if (!token.startsWith("-")) break; // a different git subcommand
    }
    const sub = tokens[i + 1];
    if (tokens[i] !== "worktree" || (sub !== "add" && sub !== "remove")) continue;
    const raw = tokens.slice(i + 2).find((token) => !token.startsWith("-"));
    ops.push({ sub, path: raw === undefined ? undefined : stripQuotes(raw), base });
  }
  return ops;
}

/** Absolute candidate targets of a mutating tool call (empty for unrecognized shapes). */
function targetsOf(tool: string, args: unknown, cwd: string): string[] {
  if (EDIT_TOOLS.has(tool)) {
    const path = stringField(args, "filePath");
    return path === undefined ? [] : [resolve(cwd, stripQuotes(path))];
  }
  const text = stringField(args, "patchText") ?? stringField(args, "patch");
  if (text === undefined) return [];
  return [...text.matchAll(PATCH_FILE_LINE)].map((m) => resolve(cwd, stripQuotes(m[1])));
}

/** Whether `target` is `root` itself or lives under it (both absolute). */
function isInside(root: string, target: string): boolean {
  return target === root || target.startsWith(root + "/");
}

/**
 * G8: while this process has seen `git worktree add ../kt-* …` and that worktree still
 * exists, edit/write/patch calls targeting the main tree are blocked — the orchestrator
 * session stays in main, so feature isolation is enforced mechanically (ADR-024).
 */
function mainTreeReadOnlyWhileFeatureGate(mainRoot: string): Gate {
  const active = new Set<string>();
  return {
    id: "G8",
    before(call) {
      if (!EDIT_TOOLS.has(call.tool) && !PATCH_TOOLS.has(call.tool)) return;
      for (const worktree of active) {
        if (!existsSync(worktree)) active.delete(worktree); // self-heal out-of-band removals
      }
      if (active.size === 0) return;
      // Relative tool paths resolve against the project directory — which is the main tree.
      if (targetsOf(call.tool, call.args, mainRoot).some((target) => isInside(mainRoot, target))) {
        throw new GateError(mainEditInstruction(active));
      }
    },
    after(call) {
      if (call.tool !== BASH_TOOL || !confirmedGreenExit(call.metadata)) return;
      const command = commandOf(call.args);
      if (command === undefined) return;
      const cwd = stringField(call.args, "workdir") ?? process.cwd();
      for (const op of worktreeOps(command)) {
        if (op.path === undefined) continue;
        const full = resolve(op.base === undefined ? cwd : resolve(cwd, op.base), op.path);
        if (op.sub === "add") active.add(full);
        else active.delete(full);
      }
    },
  };
}

// ---------------------------------------------------------------------------
// Gate G9: heavy bench invocations require the canonical flock (ADR-025)
// ---------------------------------------------------------------------------

// Make targets running hours-long boards (7×7/8×8); `make bench` (5×5/6×6) is the one exclusion.
const HEAVY_MAKE_TARGETS = new Set(["bench-deep", "bench-8x8", "bench-size"]);

// make options consuming the following token as their value.
const MAKE_VALUE_FLAGS = new Set(["-C", "--directory"]);

// flock(1) options consuming the following token as their value (spec: `-w` and friends).
const FLOCK_VALUE_FLAGS = new Set(["-w", "-W", "--timeout", "-c", "--command"]);

// The counting benchmark every heavy bench point belongs to.
const BENCH_NAME = "BenchmarkCountAllTours";

// `size<N>` / `size[...]` selector segment inside a `-bench` value (board filter).
const SIZE_SEGMENT = /size(?:\[[^\]]*\]|\d+)/g;

// Go's flag package treats `-flag` and `--flag` alike, so both dashes match; the anchor keeps
// `-benchmem`/`-benchtime` out. Capture group: inline `=<value>`, absent for the space form.
const BENCH_FLAG = /^-{1,2}bench(?:=(.*))?$/;

const HEAVY_BENCH_INSTRUCTION =
  "Gate G9: heavy benchmark runs (7×7/8×8) must be serialized under the global bench lock — " +
  "parallel feature runs distort timings and peak RSS. Rerun as " +
  "`flock ../kt-bench.lock make bench-size N=<n> DEPTHS=<d>` from the worktree root " +
  "(same wrapper for `bench-deep`, `bench-8x8` and a direct `go test -bench` selecting size7/size8). " +
  "The short `make bench` (5×5/6×6) needs no lock. ADR-025.";

/** Whether the make arguments starting at `from` list a heavy bench target; flags and `-C <dir>` pass. */
function hasHeavyMakeTarget(tokens: string[], from: number): boolean {
  for (let i = from; i < tokens.length; i += 1) {
    const token = tokens[i];
    if (MAKE_VALUE_FLAGS.has(token)) {
      i += 1; // consume the option value
      continue;
    }
    if (token.startsWith("-")) continue;
    if (HEAVY_MAKE_TARGETS.has(token)) return true;
  }
  return false;
}

/** Index of a `make` token invoking a heavy bench target, searched across the segment so wrappers (`sudo`, `env`, `time`) do not hide it; -1 otherwise. */
function heavyMakeIndex(tokens: string[]): number {
  for (let i = 0; i < tokens.length; i += 1) {
    if (tokens[i] === "make" && hasHeavyMakeTarget(tokens, i + 1)) return i;
  }
  return -1;
}

/**
 * Whether one unquoted `-bench` value selects heavy boards: a size segment with 7/8 (anonymous too —
 * BenchmarkCountAllTours is the repo's only size-segmented bench, so a false block stays in the safe
 * direction), or all boards (`.` / the named bench without a size segment). Other values are not
 * classified; anonymous selectors without a size segment (`depth22`) are an accepted edge (ADR-025).
 */
function benchValueIsHeavy(value: string): boolean {
  const v = stripQuotes(value);
  if (v === ".") return true; // matches every benchmark and board
  const sizes = [...v.matchAll(SIZE_SEGMENT)];
  if (sizes.some((m) => m[0].includes("7") || m[0].includes("8"))) return true;
  if (!v.includes(BENCH_NAME)) return false; // unrelated benchmark — not classified
  return sizes.length === 0; // the whole bench — all boards, heavy
}

/** Whether a `go test` invocation starting at `from` selects heavy boards via `-bench`/`--bench(=<value>)`. */
function goTestIsHeavy(tokens: string[], from: number): boolean {
  for (let i = from; i < tokens.length; i += 1) {
    const flag = BENCH_FLAG.exec(tokens[i]);
    if (flag === null) continue;
    const value = flag[1] ?? tokens[i + 1]; // the space form consumes the next token
    if (value !== undefined && benchValueIsHeavy(value)) return true;
  }
  return false;
}

/** Index of a `go test` token pair whose `-bench`/`--bench` value selects heavy boards; -1 otherwise. */
function heavyGoTestIndex(tokens: string[]): number {
  for (let i = 0; i + 1 < tokens.length; i += 1) {
    if (tokens[i] === "go" && tokens[i + 1] === "test" && goTestIsHeavy(tokens, i + 2)) return i;
  }
  return -1;
}

/** Index of the first heavy bench invocation in the segment; -1 when the segment is not under the gate. */
function heavyInvocationIndex(tokens: string[]): number {
  const make = heavyMakeIndex(tokens);
  const gotest = heavyGoTestIndex(tokens);
  if (make === -1) return gotest;
  if (gotest === -1) return make;
  return Math.min(make, gotest);
}

/** First non-flag argument of a `flock` invocation starting at `from` — its lock file; flags pass, value flags consume the next token. */
function flockLockFile(tokens: string[], from: number): string | undefined {
  for (let i = from; i < tokens.length; i += 1) {
    const token = tokens[i];
    if (FLOCK_VALUE_FLAGS.has(token)) {
      i += 1; // consume the option value
      continue;
    }
    if (token.startsWith("-")) continue;
    return stripQuotes(token);
  }
  return undefined;
}

/** Whether a `flock` invocation before index `heavy` guards the segment with the canonical lock file. */
function guardedByCanonicalFlock(tokens: string[], heavy: number, base: string, canonicalLock: string): boolean {
  for (let i = 0; i < heavy; i += 1) {
    if (tokens[i] !== "flock") continue;
    const lock = flockLockFile(tokens, i + 1);
    if (lock !== undefined && resolve(base, lock) === canonicalLock) return true;
  }
  return false;
}

/** Blocks the whole bash invocation when a segment runs a heavy bench without canonical flock before it. */
function requireHeavyBenchFlock(command: string | undefined, base: string, canonicalLock: string): void {
  if (command === undefined) return;
  for (const segment of splitCommand(command)) {
    const tokens = tokenize(segment.text);
    const heavy = heavyInvocationIndex(tokens);
    if (heavy === -1 || guardedByCanonicalFlock(tokens, heavy, base, canonicalLock)) continue;
    throw new GateError(HEAVY_BENCH_INSTRUCTION);
  }
}

/**
 * G9: stateless observer on bash only — a segment invoking `make bench-deep|bench-8x8|bench-size`
 * or `go test -bench` on heavy boards must be preceded in the same segment by `flock` of the
 * canonical lock `<parent-of-main>/kt-bench.lock` (relative args resolve from `workdir`, else
 * process cwd — the G8 base). Exit codes are never read; serialization itself is flock(1)'s job.
 */
function heavyBenchFlockGate(mainRoot: string): Gate {
  const canonicalLock = resolve(dirname(mainRoot), "kt-bench.lock");
  return {
    id: "G9",
    before(call) {
      if (call.tool !== BASH_TOOL) return;
      const base = stringField(call.args, "workdir") ?? process.cwd();
      requireHeavyBenchFlock(commandOf(call.args), resolve(base), canonicalLock);
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
export const GatesPlugin: Plugin = async ({ client, worktree }) => {
  // Stamp and worktree state live here for the whole server process, shared by all sessions (ADR-023/024); G9 is stateless.
  const gates: Gate[] = [
    makeCheckBeforeCommitGate(),
    mainTreeReadOnlyWhileFeatureGate(resolve(worktree)),
    heavyBenchFlockGate(resolve(worktree)),
  ];
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
