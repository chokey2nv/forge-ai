/**
 * Phase 3 replaces this with a Mongo-backed implementation behind the same interface. File checkpoints are throwaway.
 */

import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import type { AgentState } from "./agent.js";

export class FileCheckpointer {
  constructor(private dir: string, private runId: string) {}

  private path() {
    return join(this.dir, `${this.runId}.json`);
  }

  async save(state: AgentState): Promise<void> {
    await mkdir(this.dir, { recursive: true });
    await writeFile(
      this.path(),
      JSON.stringify({ runId: this.runId, state, savedAt: new Date().toISOString() }, null, 2),
    );
  }

  async load(): Promise<AgentState | null> {
    try {
      const raw = await readFile(this.path(), "utf8");
      return (JSON.parse(raw) as { state: AgentState }).state;
    } catch {
      return null;
    }
  }
}