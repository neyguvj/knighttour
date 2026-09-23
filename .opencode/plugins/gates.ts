import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { basename, dirname, extname, join, resolve } from "node:path";
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

/** A slash-command invocation observed before its model turn (hook `command.execute.before`). */
interface CommandInvocation {
  name: string;
  sessionID: string;
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
  onCommand?(invocation: CommandInvocation): void;
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
// Gate G10: a new tools/<name>.py requires its card and index row (ADR-027)
// ---------------------------------------------------------------------------

const TOOLS_DIR = "tools";

// Card-name spellings accepted for a python tool: the snake_case stem as-is and its kebab-case
// twin (`bench_table.py` ↔ `bench-table.md`); requiring one orthography would false-block.
function cardStems(stem: string): string[] {
  const kebab = stem.replaceAll("_", "-");
  return kebab === stem ? [stem] : [stem, kebab];
}

/** Builds the G10 block instruction naming where the card and the index row must appear. */
function toolCardInstruction(target: string, specsDir: string, stem: string): string {
  const cards = cardStems(stem).map((s) => `${specsDir}/${s}.md`).join(" or ");
  return (
    `Gate G10: creating ${target} is blocked — a tool exists only as code + card + index row. ` +
    `First create the card (${cards}) following the skill \`tools\` template and add a row to ` +
    `${specsDir}/README.md mentioning "${stem}", then repeat this write. ADR-027.`
  );
}

/** `<X>/specs/tools` catalogs for an absolute `…/tools/…py` target (any depth); empty for other shapes. */
function toolSpecsDirs(target: string): string[] {
  const file = basename(target);
  if (!file.endsWith(".py") || file === ".py") return [];
  const segments = target.split("/");
  const dirs: string[] = [];
  for (let i = 0; i < segments.length - 1; i += 1) {
    if (segments[i] !== TOOLS_DIR) continue;
    dirs.push(join(segments.slice(0, i).join("/"), "specs", TOOLS_DIR));
  }
  return dirs;
}

/** Whether the tools index exists and mentions one of the stem spellings (state, not format). */
function indexMentions(specsDir: string, stems: string[]): boolean {
  let text: string;
  try {
    text = readFileSync(join(specsDir, "README.md"), "utf8");
  } catch {
    return false; // missing/unreadable index counts as no mention — blocking is the safe side
  }
  return stems.some((stem) => text.includes(stem));
}

/** Whether one catalog holds a card for the stem (either spelling) and an index mention. */
function toolCatalogReady(specsDir: string, stem: string): boolean {
  const stems = cardStems(stem);
  if (!stems.some((s) => existsSync(join(specsDir, `${s}.md`)))) return false;
  return indexMentions(specsDir, stems);
}

/** Blocks a write that creates a fresh `tools/…py` whose catalog lacks the card or the index row. */
function requireToolCard(target: string): void {
  if (existsSync(target)) return; // editing an existing tool is free; Update/Delete patches exist anyway
  const specsDirs = toolSpecsDirs(target);
  if (specsDirs.length === 0) return;
  const stem = basename(target, ".py");
  if (specsDirs.some((dir) => toolCatalogReady(dir, stem))) return;
  throw new GateError(toolCardInstruction(target, specsDirs[specsDirs.length - 1], stem));
}

/**
 * G10: stateless — creating a new `tools/<name>.py` (file absent at call time) requires the card
 * `specs/tools/<stem|stem-kebab>.md` and an index mention in `<X>/specs/tools/README.md` of the
 * tree being written into (structural binding, not the session root); edits of existing tools and
 * non-`.py` files pass freely. Rename = new path, same effort; reverse order stays with review.
 */
function toolRequiresCardGate(mainRoot: string): Gate {
  return {
    id: "G10",
    before(call) {
      if (!EDIT_TOOLS.has(call.tool) && !PATCH_TOOLS.has(call.tool)) return;
      for (const target of targetsOf(call.tool, call.args, mainRoot)) {
        requireToolCard(target);
      }
    },
  };
}

// ---------------------------------------------------------------------------
// Gate G11: transit artifacts (by extension) are written only under work/ (ADR-027)
// ---------------------------------------------------------------------------

// Extension-only class: edit/write carry text, so content sniffing is pointless. The list mirrors
// the artifact lines of .gitignore plus `.log`; extending it is a decision, not a matcher patch.
const SCRATCH_EXTENSIONS = new Set([".log", ".exe", ".dll", ".so", ".dylib", ".test", ".out", ".pyc"]);

/** Builds the G11 block instruction pointing at `work/<task>/`. */
function scratchInstruction(target: string): string {
  return (
    `Gate G11: writing ${target} is blocked — transient artifacts (logs, binaries, caches; by ` +
    "extension) live under <repo>/work/<task>/ only (AGENTS.md «Working scratch space»). Rerun the " +
    "write there; if it became a reusable script, register it via skill `tools`. Bash redirections " +
    "are an accepted opaque class. ADR-027."
  );
}

/** Nearest ancestor of the target holding `.git` (dir in main, file in a worktree); undefined outside any repo. */
function repoRootOf(target: string): string | undefined {
  for (let dir = dirname(target); ; dir = dirname(dir)) {
    if (existsSync(join(dir, ".git"))) return dir;
    if (dirname(dir) === dir) return undefined;
  }
}

/** Blocks a class-extension target inside a repo tree but outside its `work/` — existing files included. */
function requireScratchZone(target: string): void {
  if (!SCRATCH_EXTENSIONS.has(extname(target).toLowerCase())) return;
  const root = repoRootOf(target);
  if (root === undefined) return; // outside any repo — external_directory/G2 territory
  if (isInside(join(root, "work"), target)) return;
  throw new GateError(scratchInstruction(target));
}

/**
 * G11: stateless — edit/write/patch targets whose extension marks a transit artifact are allowed
 * only under `<repo-root>/work/**` of the tree being written (walk-up to `.git` defines the root).
 * No existence exemption: an already-polluted path stays blocked. Bash redirections are the
 * accepted opaque class (G7/G8 precedent).
 */
function scratchStaysInWorkGate(mainRoot: string): Gate {
  return {
    id: "G11",
    before(call) {
      if (!EDIT_TOOLS.has(call.tool) && !PATCH_TOOLS.has(call.tool)) return;
      for (const target of targetsOf(call.tool, call.args, mainRoot)) {
        requireScratchZone(target);
      }
    },
  };
}

// ---------------------------------------------------------------------------
// Gate G12: a package's spec is updated before its Go code (ADR-028)
// ---------------------------------------------------------------------------

// Slash commands around the /quick bypass mark: quick marks the session, task/feature clear it.
const QUICK_COMMAND = "quick";
const SPEC_FIRST_COMMANDS = new Set(["task", "feature"]);

// Git calls cost ~8 ms in this repo (ADR-028); a hung call must never stall an edit — on any
// failure (including timeout) the verdict is unknown and the check passes (fail-open).
const GIT_TIMEOUT_MS = 5000;

/** Outcome of the "spec updated" git check; `unknown` (git failed / base unresolvable) passes. */
type SpecState = "updated" | "stale" | "unknown";

/** Builds the G12 instruction for a package whose spec file does not exist yet. */
function missingSpecInstruction(target: string, specRel: string): string {
  return (
    `Gate G12: editing ${target} is blocked — its package has no spec at ${specRel}. Create the ` +
    "spec first (AGENTS.md «Mandatory workflow» step 2), then repeat this edit. If the change is " +
    "code-only and needs no spec, run it via /quick on a chore/<slug> branch. ADR-028."
  );
}

/** Builds the G12 instruction for an existing but unmodified spec. */
function staleSpecInstruction(target: string, specRel: string): string {
  return (
    `Gate G12: editing ${target} is blocked — ${specRel} shows no pending change in this tree ` +
    "(clean working copy and unchanged since merge-base with main). Update the spec first — the " +
    "contract for your change, or a line stating behavior/API are unchanged — then repeat this " +
    "edit; or run a code-only change via /quick on a chore/<slug> branch. ADR-028."
  );
}

/** Runs git in `root`; undefined on any failure (non-zero exit, timeout, missing git). */
function gitInTree(root: string, args: string[]): string | undefined {
  try {
    return execFileSync("git", ["-C", root, ...args], {
      encoding: "utf8",
      timeout: GIT_TIMEOUT_MS,
      stdio: ["ignore", "pipe", "ignore"],
      maxBuffer: 1 << 20,
    });
  } catch {
    return undefined;
  }
}

/** `merge-base HEAD origin/main` (the A/B base of skill workflow), falling back to local `main`. */
function branchBase(root: string): string | undefined {
  for (const ref of ["origin/main", "main"]) {
    const sha = gitInTree(root, ["merge-base", "HEAD", ref])?.trim();
    if (sha !== undefined && sha !== "") return sha;
  }
  return undefined;
}

/**
 * Whether the spec counts as updated in `root` at call time: dirty worktree/index (untracked
 * included — a fresh spec) or changed since the branch base. An unresolvable base is `unknown`:
 * no false block from infrastructure gaps (ADR-028 fail-open).
 */
function specState(root: string, specRel: string): SpecState {
  const status = gitInTree(root, ["status", "--porcelain", "--", specRel]);
  if (status === undefined) return "unknown";
  if (status.trim() !== "") return "updated";
  const base = branchBase(root);
  if (base === undefined) return "unknown";
  const diff = gitInTree(root, ["diff", "--name-only", base, "--", specRel]);
  if (diff === undefined) return "unknown";
  return diff.trim() === "" ? "stale" : "updated";
}

/** Repo-relative spec path of a Go target: root package → specs/main.md, <dir>/…go → specs/<dir>.md. */
function specPathOf(root: string, target: string): string {
  const dir = dirname(target.slice(root.length + 1));
  return join("specs", (dir === "." ? "main" : dir) + ".md");
}

/** Whether a directory segment of the tree-relative path equals `name` (Go's ignored `testdata`);
 * relative to the repo root so ancestor directories of the checkout can never disable the gate. */
function underDirSegment(relPath: string, name: string): boolean {
  return dirname(relPath).split("/").includes(name);
}

/** Positive-verdict cache key: tree root + repo-relative spec path (specs/gates.md G12). */
function specCacheKey(root: string, specRel: string): string {
  return `${root}\u0000${specRel}`;
}

/** Blocks a Go source edit unless the package spec exists and is updated; verified positives cache. */
function requireFreshSpec(target: string, passed: Set<string>): void {
  if (!target.endsWith(".go") || target.endsWith("_test.go")) return;
  const root = repoRootOf(target); // walk-up to `.git`, as in G11; outside any repo — G2 territory
  if (root === undefined) return;
  if (isInside(join(root, "work"), target)) return; // scratch zone (G11's other side)
  if (underDirSegment(target.slice(root.length + 1), "testdata")) return;
  const specRel = specPathOf(root, target);
  if (passed.has(specCacheKey(root, specRel))) return;
  if (!existsSync(join(root, specRel))) throw new GateError(missingSpecInstruction(target, specRel));
  const state = specState(root, specRel);
  if (state === "updated") passed.add(specCacheKey(root, specRel));
  if (state === "stale") throw new GateError(staleSpecInstruction(target, specRel));
}

/**
 * G12: edit/write/patch of `<pkg>/*.go` (not `_test.go`, not `work/**`/`testdata/`) requires
 * `specs/<pkg>.md` dirty in the target tree or changed since merge-base with main; a missing
 * spec blocks with "create it". Positives cache per (root, spec), negatives never do; the cache
 * resets on `vcs.branch.updated`. The `/quick` command marks its session as bypassed while
 * `/task` and `/feature` clear the mark — via `command.execute.before`, because `command.executed`
 * arrives only after the whole command turn (probe, ADR-028). State is independent of G7–G11.
 */
function specBeforeCodeGate(mainRoot: string): Gate {
  const passed = new Set<string>();
  const quickSessions = new Set<string>();
  return {
    id: "G12",
    before(call) {
      if (quickSessions.has(call.sessionID)) return;
      if (!EDIT_TOOLS.has(call.tool) && !PATCH_TOOLS.has(call.tool)) return;
      for (const target of targetsOf(call.tool, call.args, mainRoot)) {
        requireFreshSpec(target, passed);
      }
    },
    onCommand(invocation) {
      if (invocation.name === QUICK_COMMAND) quickSessions.add(invocation.sessionID);
      else if (SPEC_FIRST_COMMANDS.has(invocation.name)) quickSessions.delete(invocation.sessionID);
    },
    onEvent(event) {
      if (event.type === "vcs.branch.updated") passed.clear();
    },
  };
}

// ---------------------------------------------------------------------------
// Plugin core
// ---------------------------------------------------------------------------

/**
 * Gates plugin: registers one `tool.execute.before`, one `tool.execute.after`,
 * one `command.execute.before` and one `event` handler each, dispatching to the
 * gate list in registration order. A new gate is a new list entry — the core
 * does not change.
 */
export const GatesPlugin: Plugin = async ({ client, worktree }) => {
  // Stamp and worktree state live here for the whole server process, shared by all sessions (ADR-023/024);
  // G9–G11 are stateless; G12 keeps its own session marks and spec cache (ADR-028).
  const gates: Gate[] = [
    makeCheckBeforeCommitGate(),
    mainTreeReadOnlyWhileFeatureGate(resolve(worktree)),
    heavyBenchFlockGate(resolve(worktree)),
    toolRequiresCardGate(resolve(worktree)),
    scratchStaysInWorkGate(resolve(worktree)),
    specBeforeCodeGate(resolve(worktree)),
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
    "command.execute.before": async (input) => {
      const invocation: CommandInvocation = { name: input.command, sessionID: input.sessionID };
      for (const gate of gates) {
        await runGate(gate, logFailure, () => gate.onCommand?.(invocation));
      }
    },
    event: async ({ event }) => {
      for (const gate of gates) {
        await runGate(gate, logFailure, () => gate.onEvent?.(event));
      }
    },
  };
};
