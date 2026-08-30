export type ConfigQueryFormat = "auto" | "json" | "yaml" | "toml";

export type ConfigQuerySegment = string | number | "*";

export interface ConfigQueryMatch {
  address: string;
  line: number;
  endLine: number;
  text: string;
}

export interface ConfigQueryResult {
  matches: ConfigQueryMatch[];
  warnings: string[];
}

interface LocatedConfigNode {
  segments: Array<string | number>;
  startLine: number;
  endLine: number;
  raw: string;
}

const SIMPLE_KEY = /^[A-Za-z_$][A-Za-z0-9_$-]*$/;

function configAddress(segments: Array<string | number>): string {
  let output = "$";
  for (const segment of segments) {
    if (typeof segment === "number") {
      output += `[${segment}]`;
    } else if (SIMPLE_KEY.test(segment)) {
      output += `.${segment}`;
    } else {
      output += `[${JSON.stringify(segment)}]`;
    }
  }
  return output;
}

function decodePointerSegment(value: string): string {
  return value.replaceAll("~1", "/").replaceAll("~0", "~");
}

function parseBracketSegment(raw: string): ConfigQuerySegment {
  const value = raw.trim();
  if (value === "*") return "*";
  if (/^(?:0|[1-9]\d*)$/.test(value)) return Number(value);
  if (value.startsWith('"') && value.endsWith('"')) {
    try {
      return JSON.parse(value) as string;
    } catch {
      throw new Error(`Invalid quoted configuration query segment: [${raw}]`);
    }
  }
  if (value.startsWith("'") && value.endsWith("'")) {
    return value.slice(1, -1).replace(/\\'/g, "'").replace(/\\\\/g, "\\");
  }
  if (!value) throw new Error("Empty [] segment in configuration query.");
  return value;
}

export function parseConfigQuery(query: string): ConfigQuerySegment[] {
  const value = query.trim();
  if (!value || value === "$" || value === "/") return [];
  if (value.startsWith("/")) {
    return value
      .split("/")
      .slice(1)
      .map(decodePointerSegment)
      .map((segment): ConfigQuerySegment => segment === "*" ? "*" : /^(?:0|[1-9]\d*)$/.test(segment) ? Number(segment) : segment);
  }

  const segments: ConfigQuerySegment[] = [];
  let index = value.startsWith("$") ? 1 : 0;
  if (value[index] === ".") index += 1;
  let bare = "";
  const flushBare = () => {
    if (!bare) return;
    segments.push(bare === "*" ? "*" : bare);
    bare = "";
  };

  while (index < value.length) {
    const character = value[index];
    if (character === "\\") {
      index += 1;
      if (index >= value.length) throw new Error("Configuration query ends with an incomplete escape.");
      bare += value[index];
      index += 1;
      continue;
    }
    if (character === ".") {
      flushBare();
      index += 1;
      continue;
    }
    if (character === "[") {
      flushBare();
      const start = index + 1;
      index += 1;
      let quote = "";
      let escaped = false;
      while (index < value.length) {
        const current = value[index];
        if (escaped) {
          escaped = false;
        } else if (current === "\\") {
          escaped = true;
        } else if (quote) {
          if (current === quote) quote = "";
        } else if (current === '"' || current === "'") {
          quote = current;
        } else if (current === "]") {
          break;
        }
        index += 1;
      }
      if (index >= value.length || value[index] !== "]") {
        throw new Error("Configuration query has an unclosed [ segment.");
      }
      segments.push(parseBracketSegment(value.slice(start, index)));
      index += 1;
      continue;
    }
    bare += character;
    index += 1;
  }
  flushBare();
  return segments;
}

function queryMatches(query: ConfigQuerySegment[], actual: Array<string | number>): boolean {
  if (query.length !== actual.length) return false;
  return query.every((segment, index) => segment === "*" || segment === actual[index]);
}

function summarizeRaw(value: string, max = 360): string {
  const oneLine = value.replace(/\s+/g, " ").trim();
  if (oneLine.length <= max) return oneLine;
  return `${oneLine.slice(0, max)}…`;
}

function locatedResult(nodes: LocatedConfigNode[], query: ConfigQuerySegment[]): ConfigQueryMatch[] {
  return nodes
    .filter((node) => queryMatches(query, node.segments))
    .map((node) => {
      const address = configAddress(node.segments);
      return {
        address,
        line: node.startLine,
        endLine: node.endLine,
        text: `${address} = ${summarizeRaw(node.raw)}`
      };
    })
    .sort((left, right) => left.line - right.line || left.address.localeCompare(right.address));
}

class JsonLocationParser {
  private index = 0;
  private readonly lineStarts = [0];
  readonly nodes: LocatedConfigNode[] = [];

  constructor(private readonly text: string) {
    for (let index = 0; index < text.length; index += 1) {
      if (text.charCodeAt(index) === 10) this.lineStarts.push(index + 1);
    }
  }

  private lineAt(offset: number): number {
    let low = 0;
    let high = this.lineStarts.length - 1;
    while (low <= high) {
      const midpoint = Math.floor((low + high) / 2);
      if (this.lineStarts[midpoint] <= offset) low = midpoint + 1;
      else high = midpoint - 1;
    }
    return Math.max(1, high + 1);
  }

  private fail(message: string): never {
    throw new Error(`${message} at line ${this.lineAt(this.index)}.`);
  }

  private skipTrivia(): void {
    while (this.index < this.text.length) {
      const character = this.text[this.index];
      if (/\s/.test(character)) {
        this.index += 1;
        continue;
      }
      if (character === "/" && this.text[this.index + 1] === "/") {
        this.index += 2;
        while (this.index < this.text.length && this.text[this.index] !== "\n") this.index += 1;
        continue;
      }
      if (character === "/" && this.text[this.index + 1] === "*") {
        const end = this.text.indexOf("*/", this.index + 2);
        if (end < 0) this.fail("Unclosed JSON block comment");
        this.index = end + 2;
        continue;
      }
      break;
    }
  }

  private parseString(): { value: string; start: number; end: number } {
    const start = this.index;
    if (this.text[this.index] !== '"') this.fail("Expected a JSON string");
    this.index += 1;
    let escaped = false;
    while (this.index < this.text.length) {
      const character = this.text[this.index];
      if (escaped) {
        escaped = false;
        this.index += 1;
        continue;
      }
      if (character === "\\") {
        escaped = true;
        this.index += 1;
        continue;
      }
      if (character === '"') {
        this.index += 1;
        const raw = this.text.slice(start, this.index);
        try {
          return { value: JSON.parse(raw) as string, start, end: this.index };
        } catch {
          this.fail("Invalid JSON string escape");
        }
      }
      if (character === "\n" || character === "\r") this.fail("Unclosed JSON string");
      this.index += 1;
    }
    this.fail("Unclosed JSON string");
  }

  private addNode(segments: Array<string | number>, start: number, end: number): void {
    this.nodes.push({
      segments,
      startLine: this.lineAt(start),
      endLine: this.lineAt(Math.max(start, end - 1)),
      raw: this.text.slice(start, end)
    });
  }

  private parsePrimitive(segments: Array<string | number>): void {
    const start = this.index;
    while (this.index < this.text.length && !/[\s,}\]]/.test(this.text[this.index])) this.index += 1;
    const raw = this.text.slice(start, this.index);
    if (!raw || !/^(?:true|false|null|-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?)$/.test(raw)) {
      this.fail(`Invalid JSON value ${JSON.stringify(raw)}`);
    }
    this.addNode(segments, start, this.index);
  }

  private parseObject(segments: Array<string | number>): void {
    const start = this.index;
    this.index += 1;
    this.skipTrivia();
    if (this.text[this.index] === "}") {
      this.index += 1;
      this.addNode(segments, start, this.index);
      return;
    }
    while (this.index < this.text.length) {
      this.skipTrivia();
      const key = this.parseString().value;
      this.skipTrivia();
      if (this.text[this.index] !== ":") this.fail("Expected : after JSON object key");
      this.index += 1;
      this.parseValue([...segments, key]);
      this.skipTrivia();
      if (this.text[this.index] === "}") {
        this.index += 1;
        this.addNode(segments, start, this.index);
        return;
      }
      if (this.text[this.index] !== ",") this.fail("Expected , or } in JSON object");
      this.index += 1;
      this.skipTrivia();
      if (this.text[this.index] === "}") {
        this.index += 1;
        this.addNode(segments, start, this.index);
        return;
      }
    }
    this.fail("Unclosed JSON object");
  }

  private parseArray(segments: Array<string | number>): void {
    const start = this.index;
    this.index += 1;
    this.skipTrivia();
    if (this.text[this.index] === "]") {
      this.index += 1;
      this.addNode(segments, start, this.index);
      return;
    }
    let item = 0;
    while (this.index < this.text.length) {
      this.parseValue([...segments, item]);
      item += 1;
      this.skipTrivia();
      if (this.text[this.index] === "]") {
        this.index += 1;
        this.addNode(segments, start, this.index);
        return;
      }
      if (this.text[this.index] !== ",") this.fail("Expected , or ] in JSON array");
      this.index += 1;
      this.skipTrivia();
      if (this.text[this.index] === "]") {
        this.index += 1;
        this.addNode(segments, start, this.index);
        return;
      }
    }
    this.fail("Unclosed JSON array");
  }

  private parseValue(segments: Array<string | number>): void {
    this.skipTrivia();
    const character = this.text[this.index];
    if (character === "{") {
      this.parseObject(segments);
      return;
    }
    if (character === "[") {
      this.parseArray(segments);
      return;
    }
    if (character === '"') {
      const parsed = this.parseString();
      this.addNode(segments, parsed.start, parsed.end);
      return;
    }
    this.parsePrimitive(segments);
  }

  parse(): LocatedConfigNode[] {
    this.skipTrivia();
    if (this.index >= this.text.length) this.fail("Empty JSON document");
    this.parseValue([]);
    this.skipTrivia();
    if (this.index !== this.text.length) this.fail("Unexpected content after JSON document");
    return this.nodes;
  }
}

function stripComment(value: string, marker = "#"): string {
  let quote = "";
  let escaped = false;
  let squareDepth = 0;
  let curlyDepth = 0;
  for (let index = 0; index < value.length; index += 1) {
    const character = value[index];
    if (escaped) {
      escaped = false;
      continue;
    }
    if (character === "\\" && quote === '"') {
      escaped = true;
      continue;
    }
    if (quote) {
      if (character === quote) quote = "";
      continue;
    }
    if (character === '"' || character === "'") {
      quote = character;
      continue;
    }
    if (character === "[") squareDepth += 1;
    else if (character === "]") squareDepth = Math.max(0, squareDepth - 1);
    else if (character === "{") curlyDepth += 1;
    else if (character === "}") curlyDepth = Math.max(0, curlyDepth - 1);
    else if (character === marker && squareDepth === 0 && curlyDepth === 0) return value.slice(0, index);
  }
  return value;
}

function delimiterOutsideQuotes(value: string, delimiter: string): number {
  let quote = "";
  let escaped = false;
  let squareDepth = 0;
  let curlyDepth = 0;
  for (let index = 0; index < value.length; index += 1) {
    const character = value[index];
    if (escaped) {
      escaped = false;
      continue;
    }
    if (character === "\\" && quote === '"') {
      escaped = true;
      continue;
    }
    if (quote) {
      if (character === quote) quote = "";
      continue;
    }
    if (character === '"' || character === "'") {
      quote = character;
      continue;
    }
    if (character === "[") squareDepth += 1;
    else if (character === "]") squareDepth = Math.max(0, squareDepth - 1);
    else if (character === "{") curlyDepth += 1;
    else if (character === "}") curlyDepth = Math.max(0, curlyDepth - 1);
    else if (character === delimiter && squareDepth === 0 && curlyDepth === 0) return index;
  }
  return -1;
}

function unquoteKey(value: string): string {
  const key = value.trim();
  if (key.startsWith('"') && key.endsWith('"')) {
    try {
      return JSON.parse(key) as string;
    } catch {
      return key.slice(1, -1);
    }
  }
  if (key.startsWith("'") && key.endsWith("'")) return key.slice(1, -1).replace(/''/g, "'");
  return key;
}

function parseYamlNodes(text: string): { nodes: LocatedConfigNode[]; warnings: string[] } {
  const lines = text.replace(/\r\n/g, "\n").replace(/\r/g, "\n").split("\n");
  const nodes: LocatedConfigNode[] = [{ segments: [], startLine: 1, endLine: Math.max(1, lines.length), raw: text }];
  const warnings: string[] = [];
  const stack: Array<{ indent: number; path: Array<string | number> }> = [{ indent: -1, path: [] }];
  const sequenceCounters = new Map<string, number>();

  const add = (segments: Array<string | number>, line: number, raw: string) => {
    nodes.push({ segments, startLine: line, endLine: line, raw });
  };

  for (let index = 0; index < lines.length; index += 1) {
    const rawLine = lines[index];
    if (/^\s*\t/.test(rawLine)) {
      warnings.push(`Skipped tab-indented YAML at line ${index + 1}.`);
      continue;
    }
    const indent = rawLine.match(/^ */)?.[0].length ?? 0;
    const content = stripComment(rawLine.slice(indent)).trimEnd();
    if (!content.trim() || /^(?:---|\.\.\.)\s*$/.test(content.trim())) continue;

    while (stack.length > 1 && indent <= stack[stack.length - 1].indent) stack.pop();
    const parent = stack[stack.length - 1].path;
    const trimmed = content.trimStart();

    if (trimmed === "-" || trimmed.startsWith("- ")) {
      const counterKey = `${configAddress(parent)}\0${indent}`;
      const itemIndex = sequenceCounters.get(counterKey) ?? 0;
      sequenceCounters.set(counterKey, itemIndex + 1);
      const itemPath = [...parent, itemIndex];
      const rest = trimmed.slice(1).trimStart();
      if (!rest) {
        add(itemPath, index + 1, rawLine.trim());
        stack.push({ indent, path: itemPath });
        continue;
      }
      const colon = delimiterOutsideQuotes(rest, ":");
      if (colon > 0) {
        const key = unquoteKey(rest.slice(0, colon));
        const value = rest.slice(colon + 1).trim();
        const keyPath = [...itemPath, key];
        add(keyPath, index + 1, value || rawLine.trim());
        stack.push({ indent, path: itemPath });
        if (!value || /^[|>][+-]?\d*$/.test(value)) stack.push({ indent: indent + 2, path: keyPath });
      } else {
        add(itemPath, index + 1, rest);
      }
      continue;
    }

    const colon = delimiterOutsideQuotes(trimmed, ":");
    if (colon <= 0) {
      warnings.push(`Skipped unsupported YAML syntax at line ${index + 1}.`);
      continue;
    }
    const key = unquoteKey(trimmed.slice(0, colon));
    const value = trimmed.slice(colon + 1).trim();
    const keyPath = [...parent, key];
    add(keyPath, index + 1, value || rawLine.trim());
    if (!value || /^[|>][+-]?\d*$/.test(value)) stack.push({ indent, path: keyPath });
  }

  return { nodes, warnings: [...new Set(warnings)] };
}

function parseDottedKey(value: string): string[] {
  const segments: string[] = [];
  let current = "";
  let quote = "";
  let escaped = false;
  const flush = () => {
    const segment = unquoteKey(current);
    if (!segment) throw new Error(`Invalid empty dotted key in ${value}.`);
    segments.push(segment);
    current = "";
  };
  for (let index = 0; index < value.length; index += 1) {
    const character = value[index];
    if (escaped) {
      current += character;
      escaped = false;
      continue;
    }
    if (character === "\\" && quote === '"') {
      current += character;
      escaped = true;
      continue;
    }
    if (quote) {
      current += character;
      if (character === quote) quote = "";
      continue;
    }
    if (character === '"' || character === "'") {
      quote = character;
      current += character;
      continue;
    }
    if (character === ".") {
      flush();
      continue;
    }
    current += character;
  }
  flush();
  return segments;
}

function parseTomlNodes(text: string): { nodes: LocatedConfigNode[]; warnings: string[] } {
  const lines = text.replace(/\r\n/g, "\n").replace(/\r/g, "\n").split("\n");
  const nodes: LocatedConfigNode[] = [{ segments: [], startLine: 1, endLine: Math.max(1, lines.length), raw: text }];
  const warnings: string[] = [];
  const arrayTableCounts = new Map<string, number>();
  let section: Array<string | number> = [];

  for (let index = 0; index < lines.length; index += 1) {
    const rawLine = lines[index];
    const value = stripComment(rawLine).trim();
    if (!value) continue;
    const arrayTable = /^\[\[(.+)\]\]$/.exec(value);
    if (arrayTable) {
      try {
        const base = parseDottedKey(arrayTable[1]);
        const key = configAddress(base);
        const item = arrayTableCounts.get(key) ?? 0;
        arrayTableCounts.set(key, item + 1);
        section = [...base, item];
        nodes.push({ segments: section, startLine: index + 1, endLine: index + 1, raw: value });
      } catch (error) {
        warnings.push(error instanceof Error ? `${error.message} (line ${index + 1})` : `Invalid TOML table at line ${index + 1}.`);
      }
      continue;
    }
    const table = /^\[(.+)\]$/.exec(value);
    if (table) {
      try {
        section = parseDottedKey(table[1]);
        nodes.push({ segments: section, startLine: index + 1, endLine: index + 1, raw: value });
      } catch (error) {
        warnings.push(error instanceof Error ? `${error.message} (line ${index + 1})` : `Invalid TOML table at line ${index + 1}.`);
      }
      continue;
    }
    const equals = delimiterOutsideQuotes(value, "=");
    if (equals <= 0) {
      warnings.push(`Skipped unsupported TOML syntax at line ${index + 1}.`);
      continue;
    }
    try {
      const key = parseDottedKey(value.slice(0, equals));
      const rawValue = value.slice(equals + 1).trim();
      nodes.push({
        segments: [...section, ...key],
        startLine: index + 1,
        endLine: index + 1,
        raw: rawValue || value
      });
    } catch (error) {
      warnings.push(error instanceof Error ? `${error.message} (line ${index + 1})` : `Invalid TOML key at line ${index + 1}.`);
    }
  }

  return { nodes, warnings: [...new Set(warnings)] };
}

export function inferConfigQueryFormat(filePath: string, requested: ConfigQueryFormat = "auto"): Exclude<ConfigQueryFormat, "auto"> | undefined {
  if (requested !== "auto") return requested;
  const lower = filePath.toLowerCase();
  if (lower.endsWith(".json") || lower.endsWith(".jsonc")) return "json";
  if (lower.endsWith(".yaml") || lower.endsWith(".yml")) return "yaml";
  if (lower.endsWith(".toml")) return "toml";
  return undefined;
}

export function queryConfigText(text: string, format: Exclude<ConfigQueryFormat, "auto">, query: string): ConfigQueryResult {
  const parsedQuery = parseConfigQuery(query);
  if (format === "json") {
    const parser = new JsonLocationParser(text);
    return { matches: locatedResult(parser.parse(), parsedQuery), warnings: [] };
  }
  const parsed = format === "yaml" ? parseYamlNodes(text) : parseTomlNodes(text);
  return {
    matches: locatedResult(parsed.nodes, parsedQuery),
    warnings: parsed.warnings
  };
}
