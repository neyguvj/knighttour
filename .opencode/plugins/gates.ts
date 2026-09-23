import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
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
// Gate G7: commit-creating invocations require a green `make check` content
// fingerprint (ADR-023; v2 — ADR-029)
// ---------------------------------------------------------------------------

const BASH_TOOL = "bash";

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

// Git calls cost ~8 ms in this repo (ADR-028); a hung call must never stall a tool invocation —
// on any failure (including timeout) the verdict is unknown. Callers decide: G7 fingerprinting
// blocks (fail-closed, ADR-029), G12 passes (fail-open). maxBuffer scales with `ls-files` output.
const GIT_TIMEOUT_MS = 5000;
const GIT_MAX_BUFFER = 8 << 20;

/** Runs git in `root`; undefined on any failure (non-zero exit, timeout, missing git). */
function gitInTree(root: string, args: string[]): string | undefined {
  try {
    return execFileSync("git", ["-C", root, ...args], {
      encoding: "utf8",
      timeout: GIT_TIMEOUT_MS,
      stdio: ["ignore", "pipe", "ignore"],
      maxBuffer: GIT_MAX_BUFFER,
    });
  } catch {
    return undefined;
  }
}

// Subcommands that create a commit (specs/gates.md G7 v2): the explicit `commit` plus the
// auto-commit ones. Forms that provably create no commit are exempted in the matcher: any of
// them with `--no-commit`, and `merge --squash` / `merge --ff-only`.
const COMMIT_SUBCOMMANDS = new Set(["commit", "merge", "cherry-pick", "revert", "rebase", "am"]);

// Sentinel for "no such entry": hex digests and blob OIDs never contain `-`, so a missing
// working copy (deleted/unreadable) and an unstaged path share one impossible value.
const NO_ENTRY = "-";

const COMMIT_INSTRUCTION =
  "Gate G7: 'git commit' is blocked — the target tree's content does not match any green " +
  "`make check` fingerprint recorded in this opencode process (ADR-029). Run `make check` in " +
  "the target tree, confirm it exits 0, then repeat the commit; or chain it self-guarded: " +
  '`make check && git commit -m "..."`. Staging checked content (`git add`) is fine; any other ' +
  "change since the check needs a fresh one. A block right after a green check means git could " +
  "not describe the target tree — the gate fails closed.";

// Auto-commit subcommands get the two-step detour: a check before the merge cannot attest what
// the merge commits, so the block instruction unfolds `--no-commit` → `make check` → commit.
const AUTO_COMMIT_INSTRUCTION =
  COMMIT_INSTRUCTION +
  " A pre-merge check does not attest an auto-committing merge/cherry-pick/revert/rebase/am: " +
  "rerun it with `--no-commit` (or `merge --squash`), then `make check`, then an explicit `git commit`.";

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

/** Parsed `make check` segment; `dir` is its last `-C <dir>` — the invocation base. */
interface MakeCheck {
  dir: string | undefined;
}

/** Segment invokes `make` with target `check`; `make` flags and `-C <dir>` are allowed. */
function makeCheckOf(segment: string): MakeCheck | undefined {
  const tokens = tokenize(segment);
  let i = skipEnvPrefix(tokens, 0);
  if (tokens[i] !== "make") return undefined;
  let dir: string | undefined;
  for (i += 1; i < tokens.length; i += 1) {
    const token = tokens[i];
    if (token === "-C" || token === "--directory") {
      if (tokens[i + 1] !== undefined) dir = stripQuotes(tokens[i + 1]);
      i += 1; // consume the directory value
      continue;
    }
    if (token.startsWith("-")) continue;
    if (token === "check") return { dir };
  }
  return undefined;
}

/** Segment invokes `make` with target `check`; flags and `-C <dir>` are allowed. */
function isMakeCheckSegment(segment: string): boolean {
  return makeCheckOf(segment) !== undefined;
}

/** A commit-creating git invocation found in one command segment. */
interface CommitInvocation {
  autoCommit: boolean; // merge/cherry-pick/revert/rebase/am — the block adds the two-step detour
  dir: string | undefined; // value of the last git `-C <dir>` flag, if any
}

/**
 * The commit-creating `git` invocation in a segment (token matcher as in v1: env prefixes and
 * global flags pass), or undefined when the segment does not create a commit — including the
 * provably non-committing forms `--no-commit`, `merge --squash` and `merge --ff-only`.
 */
function commitInvocation(segment: string): CommitInvocation | undefined {
  const tokens = tokenize(segment);
  let i = skipEnvPrefix(tokens, 0);
  if (tokens[i] !== "git") return undefined;
  let dir: string | undefined;
  for (i += 1; i < tokens.length; i += 1) {
    const token = tokens[i];
    if (GIT_VALUE_FLAGS.has(token)) {
      if (token === "-C" && tokens[i + 1] !== undefined) dir = stripQuotes(tokens[i + 1]);
      i += 1; // consume the option value
      continue;
    }
    if (token.startsWith("-")) continue;
    if (!COMMIT_SUBCOMMANDS.has(token)) return undefined;
    return exemptFromCommit(tokens, i) ? undefined : { autoCommit: token !== "commit", dir };
  }
  return undefined;
}

/** Post-subcommand flags consuming the next token as their value (commit messages etc.). */
const MESSAGE_VALUE_FLAGS = new Set(["-m", "--message", "-F", "--file"]);

/**
 * Tokens of a raw run regrouped quote-aware like shell argv: quotes group and disappear, so a
 * multi-word `-m "..."` message is one token and hides its literals from the exemption scan.
 * Undefined when quoting is unbalanced — then no exemption can be trusted (conservative).
 */
function topLevelTokens(raw: string): string[] | undefined {
  const tokens: string[] = [];
  let current = "";
  let quote: string | undefined;
  for (const ch of raw) {
    if (quote !== undefined) {
      if (ch === quote) quote = undefined;
      else current += ch;
    } else if (ch === '"' || ch === "'") {
      quote = ch;
    } else if (/\s/.test(ch)) {
      if (current !== "") tokens.push(current);
      current = "";
    } else {
      current += ch;
    }
  }
  if (quote !== undefined) return undefined;
  if (current !== "") tokens.push(current);
  return tokens;
}

/** Whether the subcommand at `at` is told to not create a commit (exemption forms, specs/gates.md). */
function exemptFromCommit(tokens: string[], at: number): boolean {
  // Exemptions are recognized outside quotes only: `-m "fix --no-commit handling"` commits.
  const rest = topLevelTokens(tokens.slice(at + 1).join(" "));
  if (rest === undefined) return false; // unbalanced quoting — keep the invocation gated
  for (let j = 0; j < rest.length; j += 1) {
    const token = rest[j];
    // A message value is text, not a flag: `git commit -m --no-commit` commits (git parses it so).
    if (MESSAGE_VALUE_FLAGS.has(token)) {
      j += 1; // consume the option value
      continue;
    }
    if (token === "--no-commit") return true;
    if (tokens[at] === "merge" && (token === "--squash" || token === "--ff-only")) return true;
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

/** The last `make check` segment of a command: its index and `-C <dir>` base (the stamp target). */
interface GreenCheck {
  index: number;
  dir: string | undefined;
}

/** Last `make check` in the segments (single parse per segment, newest first); none → undefined. */
function lastMakeCheck(segments: Segment[]): GreenCheck | undefined {
  for (let index = segments.length - 1; index >= 0; index -= 1) {
    const check = makeCheckOf(segments[index].text);
    if (check !== undefined) return { index, dir: check.dir };
  }
  return undefined;
}

/**
 * Whether a whole-line exit 0 confirms the check at `check`: true only when every segment after
 * it is joined by `&&`; `; | ||` make the reported code belong to a later command, so success
 * would be unconfirmed (ADR-023: no stamp without confirmation).
 */
function lineExitConfirmsCheck(segments: Segment[], check: number): boolean {
  return segments.slice(check + 1).every((segment) => segment.sepBefore === "&&");
}

/**
 * Whether the commit-creating segment sits at the end of an all-`&&` chain containing a
 * `make check`: on a red check the chain aborts before the commit, so no stamp is needed
 * (specs/gates.md). For auto-commit subcommands the exception is formal — the matcher grants
 * it by syntax; only the block instruction carries the semantic caveat.
 */
function guardedByCheck(segments: Segment[], commit: number): boolean {
  let start = commit;
  while (start > 0 && segments[start].sepBefore === "&&") start -= 1;
  return segments.slice(start, commit).some((segment) => isMakeCheckSegment(segment.text));
}

/** Content record of one tree path at stamp time. */
interface PathRecord {
  work: string; // sha256 of the working-copy bytes (NO_ENTRY when unreadable/deleted)
  index: string; // staged blob OID from `git ls-files -s` (NO_ENTRY when untracked)
}

/** A tree fingerprint: every non-ignored path → content record (specs/gates.md G7 "Отпечаток"). */
type TreeFingerprint = Map<string, PathRecord>;

/** sha256 of the file bytes; NO_ENTRY when unreadable — a distinct state from any content. */
function contentHash(path: string): string {
  try {
    return createHash("sha256").update(readFileSync(path)).digest("hex");
  } catch {
    return NO_ENTRY;
  }
}

/**
 * Content fingerprint of the repo at `root`: every non-ignored path (`tracked ∪ untracked`;
 * gitignore entries like `work/**` stay out — they take no part in the check) mapped to its
 * working-copy hash and staged blob OID. Undefined on any git failure — callers fail closed.
 */
function treeFingerprint(root: string): TreeFingerprint | undefined {
  const listed = gitInTree(root, ["ls-files", "-s", "-o", "--exclude-standard", "-z"]);
  if (listed === undefined) return undefined;
  const fingerprint: TreeFingerprint = new Map();
  for (const entry of listed.split("\0")) {
    if (entry === "") continue;
    // `-s` entries carry `<mode> <oid> <stage>\t<path>`; `-o` (untracked) entries are bare paths.
    const tab = entry.indexOf("\t");
    const index = tab === -1 ? NO_ENTRY : entry.slice(0, tab).split(" ")[1];
    const path = tab === -1 ? entry : entry.slice(tab + 1);
    fingerprint.set(path, { work: contentHash(join(root, path)), index });
  }
  return fingerprint;
}

/** Paths whose working copy differs from the index (`git diff-files`); undefined on git failure. */
function unstagedChanges(root: string): Set<string> | undefined {
  const listed = gitInTree(root, ["diff-files", "-z", "--name-only"]);
  if (listed === undefined) return undefined;
  return new Set(listed.split("\0").filter((path) => path !== ""));
}

/**
 * Whether the current tree still matches a stamped fingerprint (specs/gates.md G7): same path
 * set, every working copy unchanged, and each index entry either unchanged or moved to exactly
 * the checked content — `git add` between check and commit stages what was tested; any other
 * index move (foreign blob, dropped entry) changes what would be committed.
 */
function fingerprintMatches(stamped: TreeFingerprint, current: TreeFingerprint, root: string): boolean {
  if (stamped.size !== current.size) return false;
  const moved: Array<[string, PathRecord]> = [];
  for (const [path, want] of stamped) {
    const now = current.get(path);
    if (now === undefined || now.work !== want.work) return false;
    if (now.index !== want.index) moved.push([path, now]);
  }
  if (moved.length === 0) return true;
  const unstaged = unstagedChanges(root); // legal moves are staged-clean: absent from this set
  if (unstaged === undefined) return false;
  return moved.every(([path, now]) => now.index !== NO_ENTRY && !unstaged.has(path));
}

/** Repo holding the tree an invocation acts on: git `-C <dir>` over `workdir`/cwd, then walk-up. */
function invocationRepo(dir: string | undefined, workdir: string | undefined): string | undefined {
  const base = resolve(workdir ?? process.cwd());
  return repoRootAt(dir === undefined ? base : resolve(base, dir));
}

/** Whether the repo tree currently matches one recorded green fingerprint (fail-closed on gaps). */
function treeIsGreen(root: string | undefined, green: Set<TreeFingerprint>): boolean {
  if (root === undefined) return false; // outside any repo — nothing was checked here
  const current = treeFingerprint(root);
  if (current === undefined) return false; // git failure — fail-closed (ADR-029)
  for (const stamped of green) {
    if (fingerprintMatches(stamped, current, root)) return true;
  }
  return false;
}

/** Blocks every commit-creating segment whose target tree does not match a green fingerprint. */
function requireGreenFingerprint(
  command: string | undefined,
  workdir: string | undefined,
  green: Set<TreeFingerprint>,
): void {
  if (command === undefined) return;
  const segments = splitCommand(command);
  for (let i = 0; i < segments.length; i += 1) {
    const invocation = commitInvocation(segments[i].text);
    if (invocation === undefined || guardedByCheck(segments, i)) continue;
    // Each creating invocation is checked against its own target tree (specs/gates.md G7).
    if (treeIsGreen(invocationRepo(invocation.dir, workdir), green)) continue;
    throw new GateError(invocation.autoCommit ? AUTO_COMMIT_INSTRUCTION : COMMIT_INSTRUCTION);
  }
}

// Green stamps live for the process lifetime; a bounded FIFO keeps memory flat in long-running
// orchestrators — evicting the oldest fingerprint only ever costs one extra `make check` (specs/gates.md).
const GREEN_STAMP_LIMIT = 64;

/** Records the fingerprint of the tree `make check` physically ran in; no repo/git gap → no stamp. */
function recordGreenStamp(dir: string | undefined, workdir: string | undefined, green: Set<TreeFingerprint>): void {
  const root = invocationRepo(dir, workdir);
  if (root === undefined) return; // not a repo — no stamp (safe direction)
  const fingerprint = treeFingerprint(root);
  if (fingerprint === undefined) return;
  green.add(fingerprint);
  while (green.size > GREEN_STAMP_LIMIT) {
    const oldest = green.values().next();
    if (oldest.done === true) break; // unreachable while size > limit ≥ 1
    green.delete(oldest.value);
  }
}

/**
 * G7 v2: the stamp is a set of content fingerprints, not a flag. A green `make check` (bash,
 * exit-0 confirmed) records the fingerprint of the tree it ran in; a commit-creating invocation
 * passes only while its target tree still matches one recorded fingerprint — or it sits at the
 * end of an all-`&&` chain started by `make check`. Invalidation by edit/write/patch tools and
 * `file.edited` is gone: content comparison subsumes it, edits by any means (bash included)
 * surface as a fingerprint mismatch at commit time (ADR-029).
 */
function makeCheckBeforeCommitGate(): Gate {
  const green = new Set<TreeFingerprint>();
  return {
    id: "G7",
    before(call) {
      if (call.tool !== BASH_TOOL) return;
      requireGreenFingerprint(commandOf(call.args), stringField(call.args, "workdir"), green);
    },
    after(call) {
      if (call.tool !== BASH_TOOL || !confirmedGreenExit(call.metadata)) return;
      const command = commandOf(call.args);
      if (command === undefined) return;
      const segments = splitCommand(command);
      const check = lastMakeCheck(segments);
      if (check === undefined || !lineExitConfirmsCheck(segments, check.index)) return;
      recordGreenStamp(check.dir, stringField(call.args, "workdir"), green);
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

/** The repo holding the directory `dir` — walk-up including `dir` itself; undefined outside any repo. */
function repoRootAt(dir: string): string | undefined {
  for (let d = dir; ; d = dirname(d)) {
    if (existsSync(join(d, ".git"))) return d;
    if (dirname(d) === d) return undefined;
  }
}

/** Nearest ancestor of a written target file holding `.git` (dir in main, file in a worktree). */
function repoRootOf(target: string): string | undefined {
  return repoRootAt(dirname(target));
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
  // Green fingerprints and active-worktree state live here for the whole server process, shared
  // by all sessions (ADR-023/024/029); G9–G11 are stateless; G12 keeps its own session marks and
  // spec cache (ADR-028).
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
