import { randomUUID } from "node:crypto";
import type { ClockSample, IterationRecord, WorkClock } from "./types.js";

/** Epoch is deliberately per process: continuity after a crash is not guessed. */
export class ServerWorkClock implements WorkClock {
  private readonly epoch = randomUUID();
  sample(): ClockSample {
    return { epoch: this.epoch, monotonic_ms: Number(process.hrtime.bigint() / 1_000_000n), wall_ms: Date.now() };
  }
}

export function elapsedTick(iteration: IterationRecord, now: ClockSample, idleLimit: number): number {
  if (iteration.clock.epoch !== now.epoch) {
    iteration.clock_gap = true;
    iteration.clock = now;
    return 0;
  }
  const delta = Math.max(0, now.monotonic_ms - iteration.clock.monotonic_ms);
  const measured = Math.min(delta, Math.max(0, idleLimit - iteration.idle_ms));
  iteration.measured_ms += measured;
  iteration.idle_ms += delta;
  iteration.clock = now;
  return measured;
}
