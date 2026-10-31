// Private baseline-only profiler. Never imported by the deployed service.
import inspector from "node:inspector";
import fs from "node:fs";
import { pathToFileURL } from "node:url";

const [entry, destination] = process.argv.slice(2);
if (!entry || !destination || !destination.includes("codexpro-go-proof")) {
  throw new Error("Explicit private release entry and profile destination are required");
}
const session = new inspector.Session();
session.connect();
const post = (method, params = {}) => new Promise((resolve, reject) => {
  session.post(method, params, (error, result) => error ? reject(error) : resolve(result));
});
await post("Profiler.enable");
await post("Profiler.setSamplingInterval", { interval: 1000 });
await post("Profiler.start");
let stopping = false;
async function finish() {
  if (stopping) return;
  stopping = true;
  try {
    const result = await post("Profiler.stop");
    fs.writeFileSync(destination, JSON.stringify(result.profile), { mode: 0o600 });
    session.disconnect();
    process.exit(0);
  } catch (error) {
    console.error("Private profiler failed:", error.message);
    process.exit(1);
  }
}
process.on("SIGUSR2", finish);
process.on("SIGTERM", finish);
process.argv = [process.execPath, entry];
await import(pathToFileURL(entry).href);
