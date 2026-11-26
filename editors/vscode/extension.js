const vscode = require("vscode");
const { spawn } = require("child_process");
const fs = require("fs");
const path = require("path");

class EaseLspClient {
  constructor(command, outputChannel, diagnostics) {
    this.command = command;
    this.output = outputChannel;
    this.diagnostics = diagnostics;
    this.proc = null;
    this.nextId = 1;
    this.pending = new Map();
    this.buffer = Buffer.alloc(0);
    this.ready = false;
  }

  async start() {
    this.proc = spawn(this.command, ["lsp"], { stdio: ["pipe", "pipe", "pipe"] });

    this.proc.on("error", (err) => {
      this.output.appendLine(`[ease-lsp] failed to start "${this.command}": ${err.message}`);
      vscode.window.showWarningMessage(
        `Ease LSP could not start "${this.command} lsp". Set "ease.executablePath" in VS Code settings.`
      );
    });

    this.proc.stderr.on("data", (chunk) => {
      this.output.appendLine(`[ease-lsp stderr] ${chunk.toString("utf8").trimEnd()}`);
    });

    this.proc.stdout.on("data", (chunk) => this.onData(chunk));

    this.proc.on("exit", (code) => {
      this.output.appendLine(`[ease-lsp] exited with code ${code}`);
      this.ready = false;
      for (const { reject } of this.pending.values()) {
        reject(new Error("ease lsp exited"));
      }
      this.pending.clear();
    });

    await this.sendRequest("initialize", {
      processId: process.pid,
      rootUri: vscode.workspace.workspaceFolders?.[0]?.uri.toString() ?? null,
      capabilities: {},
    });
    this.sendNotification("initialized", {});
    this.ready = true;
  }

  stop() {
    if (!this.proc) return;
    try {
      this.sendNotification("exit", {});
    } catch (_) {}
    this.proc.kill();
    this.proc = null;
  }

  onData(chunk) {
    this.buffer = Buffer.concat([this.buffer, chunk]);
    while (true) {
      const headerEnd = this.buffer.indexOf("\r\n\r\n");
      if (headerEnd === -1) break;

      const headerStr = this.buffer.slice(0, headerEnd).toString("ascii");
      const match = /Content-Length:\s*(\d+)/i.exec(headerStr);
      if (!match) {
        this.buffer = this.buffer.slice(headerEnd + 4);
        continue;
      }

      const contentLength = parseInt(match[1], 10);
      const totalLen = headerEnd + 4 + contentLength;
      if (this.buffer.length < totalLen) break;

      const body = this.buffer.slice(headerEnd + 4, totalLen).toString("utf8");
      this.buffer = this.buffer.slice(totalLen);

      try {
        const msg = JSON.parse(body);
        this.handleMessage(msg);
      } catch (err) {
        this.output.appendLine(`[ease-lsp] invalid JSON: ${err.message}`);
      }
    }
  }

  handleMessage(msg) {
    if (msg.id !== undefined && this.pending.has(msg.id)) {
      const { resolve, reject } = this.pending.get(msg.id);
      this.pending.delete(msg.id);
      if (msg.error) reject(new Error(msg.error.message));
      else resolve(msg.result);
      return;
    }

    if (msg.method === "textDocument/publishDiagnostics" && msg.params) {
      const uri = vscode.Uri.parse(msg.params.uri);
      const diags = (msg.params.diagnostics || []).map((d) => {
        const range = new vscode.Range(
          d.range.start.line,
          d.range.start.character,
          d.range.end.line,
          d.range.end.character
        );
        const sev =
          d.severity === 2
            ? vscode.DiagnosticSeverity.Warning
            : vscode.DiagnosticSeverity.Error;
        const diag = new vscode.Diagnostic(range, d.message, sev);
        diag.source = d.source || "ease";
        return diag;
      });
      this.diagnostics.set(uri, diags);
    }
  }

  sendRequest(method, params) {
    if (!this.proc || !this.proc.stdin.writable) {
      return Promise.reject(new Error("ease lsp not running"));
    }
    const id = this.nextId++;
    const payload = JSON.stringify({ jsonrpc: "2.0", id, method, params });
    const frame = `Content-Length: ${Buffer.byteLength(payload, "utf8")}\r\n\r\n${payload}`;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.proc.stdin.write(frame);
    });
  }

  sendNotification(method, params) {
    if (!this.proc || !this.proc.stdin.writable) return;
    const payload = JSON.stringify({ jsonrpc: "2.0", method, params });
    const frame = `Content-Length: ${Buffer.byteLength(payload, "utf8")}\r\n\r\n${payload}`;
    this.proc.stdin.write(frame);
  }
}

function isExecutableFile(p) {
  try {
    return fs.existsSync(p) && fs.statSync(p).isFile();
  } catch (_) {
    return false;
  }
}

function findOnPath(binName) {
  const pathEnv = process.env.PATH || "";
  const dirs = pathEnv.split(path.delimiter).filter(Boolean);
  const candidates = [binName];
  if (process.platform === "win32" && !path.extname(binName)) {
    candidates.unshift(`${binName}.exe`);
  }
  for (const dir of dirs) {
    for (const name of candidates) {
      const full = path.join(dir, name);
      if (isExecutableFile(full)) {
        return full;
      }
    }
  }
  return null;
}

function resolveExecutable(filePath) {
  const cfg = vscode.workspace.getConfiguration("ease");
  const searched = [];
  const exeName = process.platform === "win32" ? "ease.exe" : "ease";

  const checkConfigured = (raw, label) => {
    const val = (raw || "").trim();
    if (!val || val === "ease" || val === "ease.exe") return null;
    if (val.includes("/") || val.includes("\\")) {
      const bases = path.isAbsolute(val)
        ? [""]
        : [
            ...(filePath ? [path.dirname(filePath)] : []),
            ...(vscode.workspace.workspaceFolders || []).map((wf) => wf.uri.fsPath),
          ];
      for (const base of bases) {
        const candidate = base ? path.resolve(base, val) : val;
        searched.push(`${label}: ${candidate}`);
        if (isExecutableFile(candidate)) return candidate;
        if (process.platform === "win32" && !candidate.toLowerCase().endsWith(".exe")) {
          const withExe = `${candidate}.exe`;
          if (isExecutableFile(withExe)) return withExe;
        }
        try {
          if (fs.existsSync(candidate) && fs.statSync(candidate).isDirectory()) {
            const inDir = path.join(candidate, exeName);
            searched.push(`${label}: ${inDir}`);
            if (isExecutableFile(inDir)) return inDir;
          }
        } catch (_) {}
      }
      return null;
    }
    const found = findOnPath(val);
    searched.push(`${label}: ${val} on PATH`);
    return found;
  };

  const fromExecCfg = checkConfigured(cfg.get("executablePath", ""), "ease.executablePath");
  if (fromExecCfg) return { exe: fromExecCfg, searched };

  const fromServerCfg = checkConfigured(cfg.get("serverPath", ""), "ease.serverPath");
  if (fromServerCfg) return { exe: fromServerCfg, searched };

  if (filePath) {
    const fileDirBin = path.join(path.dirname(filePath), exeName);
    searched.push(fileDirBin);
    if (fs.existsSync(fileDirBin)) {
      return { exe: fileDirBin, searched };
    }
  }

  for (const wf of vscode.workspace.workspaceFolders || []) {
    const wsBin = path.join(wf.uri.fsPath, exeName);
    searched.push(wsBin);
    if (fs.existsSync(wsBin)) {
      return { exe: wsBin, searched };
    }
  }

  const onPath = findOnPath(exeName);
  searched.push(`${exeName} on PATH`);
  if (onPath) {
    return { exe: onPath, searched };
  }

  return { exe: null, searched };
}

function formatTerminalCommand(exe, subcommand, filePath) {
  const shell = (vscode.env.shell || "").toLowerCase();
  const isPowerShell =
    shell.includes("powershell") ||
    shell.includes("pwsh") ||
    (process.platform === "win32" && !shell.includes("cmd") && !shell.includes("bash") && !shell.includes("sh"));
  if (isPowerShell) {
    return `& "${exe}" ${subcommand} "${filePath}"`;
  }
  return `"${exe}" ${subcommand} "${filePath}"`;
}

async function runEaseSubcommand(subcommand, targetArg) {
  let filePath =
    typeof targetArg === "string"
      ? targetArg
      : targetArg && typeof targetArg.fsPath === "string"
      ? targetArg.fsPath
      : undefined;

  const activeEditor = vscode.window.activeTextEditor;
  if (!filePath && activeEditor && activeEditor.document.languageId === "ease") {
    if (activeEditor.document.isUntitled) {
      vscode.window.showWarningMessage("Save the .ease file before running it.");
      return;
    }
    if (activeEditor.document.isDirty) {
      await activeEditor.document.save();
    }
    filePath = activeEditor.document.uri.fsPath;
  } else if (filePath && activeEditor && activeEditor.document.uri.fsPath === filePath && activeEditor.document.isDirty) {
    await activeEditor.document.save();
  }

  if (!filePath) {
    vscode.window.showWarningMessage("No active .ease file to run.");
    return;
  }

  const { exe, searched } = resolveExecutable(filePath);
  if (!exe) {
    vscode.window.showErrorMessage(
      `Could not find 'ease' executable. Looked in: ${searched.join(", ")}. Set "ease.executablePath" in VS Code settings or add ease to PATH.`
    );
    return;
  }

  const cwd = path.dirname(filePath);
  const terminal = vscode.window.createTerminal({ name: "Ease", cwd });
  terminal.show(false);
  terminal.sendText(formatTerminalCommand(exe, subcommand, filePath));
}

async function activate(context) {
  const output = vscode.window.createOutputChannel("Ease Language");
  const diagnostics = vscode.languages.createDiagnosticCollection("ease");
  const initialFile = vscode.window.activeTextEditor?.document?.uri?.fsPath;
  const { exe: initialExe, searched: initialSearched } = resolveExecutable(initialFile);
  const client = initialExe ? new EaseLspClient(initialExe, output, diagnostics) : null;

  const debugProvider = {
    provideDebugConfigurations() {
      return [
        {
          type: "ease",
          request: "launch",
          name: "Debug Ease File",
          program: "${file}",
          args: [],
          stopOnEntry: false,
        },
        {
          type: "ease",
          request: "launch",
          name: "Debug Ease File (Stop on Entry)",
          program: "${file}",
          args: [],
          stopOnEntry: true,
        },
      ];
    },
    resolveDebugConfiguration(folder, config) {
      const active = vscode.window.activeTextEditor;
      const resolved = Object.assign(
        {
          type: "ease",
          request: "launch",
          name: "Debug Ease File",
          program: "${file}",
          args: [],
          stopOnEntry: false,
        },
        config || {}
      );
      if (!resolved.program || resolved.program === "${file}") {
        if (active && active.document.languageId === "ease") {
          resolved.program = active.document.uri.fsPath;
        }
      } else if (folder && resolved.program.includes("${workspaceFolder}")) {
        resolved.program = resolved.program.replace("${workspaceFolder}", folder.uri.fsPath);
      }
      return resolved;
    },
  };

  context.subscriptions.push(
    output,
    diagnostics,
    { dispose: () => client && client.stop() },
    vscode.commands.registerCommand("ease.runFile", (uri) => runEaseSubcommand("run", uri)),
    vscode.commands.registerCommand("ease.testFile", (uri) => runEaseSubcommand("test", uri)),
    vscode.debug.registerDebugAdapterDescriptorFactory("ease", {
      createDebugAdapterDescriptor(session) {
        const targetFile =
          session?.configuration?.program || vscode.window.activeTextEditor?.document?.uri?.fsPath;
        const { exe, searched } = resolveExecutable(targetFile);
        if (!exe) {
          throw new Error(`Could not find 'ease' executable. Looked in: ${searched.join(", ")}.`);
        }
        return new vscode.DebugAdapterExecutable(exe, ["dap"]);
      },
    }),
    vscode.debug.registerDebugConfigurationProvider(
      "ease",
      debugProvider,
      vscode.DebugConfigurationProviderTriggerKind.Initial
    ),
    vscode.debug.registerDebugConfigurationProvider(
      "ease",
      debugProvider,
      vscode.DebugConfigurationProviderTriggerKind.Dynamic
    )
  );

  if (!client) {
    output.appendLine(`[ease-lsp] 'ease' executable not found. Looked in: ${initialSearched.join(", ")}`);
    return;
  }

  try {
    await client.start();
  } catch (err) {
    output.appendLine(`[ease-lsp] startup error: ${err.message}`);
    return;
  }

  const syncOpenDoc = (doc) => {
    if (doc.languageId !== "ease") return;
    client.sendNotification("textDocument/didOpen", {
      textDocument: {
        uri: doc.uri.toString(),
        languageId: "ease",
        version: doc.version,
        text: doc.getText(),
      },
    });
  };

  for (const doc of vscode.workspace.textDocuments) {
    syncOpenDoc(doc);
  }

  context.subscriptions.push(
    vscode.workspace.onDidOpenTextDocument(syncOpenDoc),
    vscode.workspace.onDidChangeTextDocument((e) => {
      if (e.document.languageId !== "ease") return;
      client.sendNotification("textDocument/didChange", {
        textDocument: { uri: e.document.uri.toString(), version: e.document.version },
        contentChanges: [{ text: e.document.getText() }],
      });
    }),
    vscode.workspace.onDidCloseTextDocument((doc) => {
      if (doc.languageId !== "ease") return;
      client.sendNotification("textDocument/didClose", {
        textDocument: { uri: doc.uri.toString() },
      });
    }),
    vscode.languages.registerDocumentSymbolProvider("ease", {
      async provideDocumentSymbols(doc) {
        if (!client.ready) return [];
        const res = await client.sendRequest("textDocument/documentSymbol", {
          textDocument: { uri: doc.uri.toString() },
        });
        return (res || []).map(toVsCodeSymbol);
      },
    }),
    vscode.languages.registerCompletionItemProvider("ease", {
      async provideCompletionItems(doc) {
        if (!client.ready) return [];
        const res = await client.sendRequest("textDocument/completion", {
          textDocument: { uri: doc.uri.toString() },
        });
        return (res || []).map((item) => {
          const ci = new vscode.CompletionItem(item.label, mapCompletionKind(item.kind));
          if (item.detail) ci.detail = item.detail;
          return ci;
        });
      },
    }),
    vscode.languages.registerDefinitionProvider("ease", {
      async provideDefinition(doc, position) {
        if (!client.ready) return [];
        const res = await client.sendRequest("textDocument/definition", {
          textDocument: { uri: doc.uri.toString() },
          position: { line: position.line, character: position.character },
        });
        return (res || []).map(toVsCodeLocation);
      },
    }),
    vscode.languages.registerHoverProvider("ease", {
      async provideHover(doc, position) {
        if (!client.ready) return null;
        const res = await client.sendRequest("textDocument/hover", {
          textDocument: { uri: doc.uri.toString() },
          position: { line: position.line, character: position.character },
        });
        if (!res || !res.contents) return null;
        const md = new vscode.MarkdownString(res.contents.value || "");
        if (res.range) {
          const r = new vscode.Range(
            res.range.start.line,
            res.range.start.character,
            res.range.end.line,
            res.range.end.character
          );
          return new vscode.Hover(md, r);
        }
        return new vscode.Hover(md);
      },
    }),
    vscode.languages.registerReferenceProvider("ease", {
      async provideReferences(doc, position, context) {
        if (!client.ready) return [];
        const res = await client.sendRequest("textDocument/references", {
          textDocument: { uri: doc.uri.toString() },
          position: { line: position.line, character: position.character },
          context: { includeDeclaration: Boolean(context?.includeDeclaration) },
        });
        return (res || []).map(toVsCodeLocation);
      },
    })
  );
}

function toVsCodeLocation(loc) {
  const uri = vscode.Uri.parse(loc.uri);
  const range = new vscode.Range(
    loc.range.start.line,
    loc.range.start.character,
    loc.range.end.line,
    loc.range.end.character
  );
  return new vscode.Location(uri, range);
}

function toVsCodeSymbol(sym) {
  const range = new vscode.Range(
    sym.range.start.line,
    sym.range.start.character,
    sym.range.end.line,
    sym.range.end.character
  );
  const sel = new vscode.Range(
    sym.selectionRange.start.line,
    sym.selectionRange.start.character,
    sym.selectionRange.end.line,
    sym.selectionRange.end.character
  );
  const kind =
    sym.kind === 12
      ? vscode.SymbolKind.Function
      : sym.kind === 23
      ? vscode.SymbolKind.Struct
      : sym.kind === 10
      ? vscode.SymbolKind.Enum
      : sym.kind === 8
      ? vscode.SymbolKind.Field
      : sym.kind === 2
      ? vscode.SymbolKind.Module
      : sym.kind === 14
      ? vscode.SymbolKind.Constant
      : vscode.SymbolKind.Namespace;
  const out = new vscode.DocumentSymbol(sym.name, sym.detail || "", kind, range, sel);
  if (Array.isArray(sym.children)) {
    out.children = sym.children.map(toVsCodeSymbol);
  }
  return out;
}

function mapCompletionKind(lspKind) {
  if (lspKind === 3) return vscode.CompletionItemKind.Function;
  if (lspKind === 22) return vscode.CompletionItemKind.Struct;
  if (lspKind === 13) return vscode.CompletionItemKind.Enum;
  return vscode.CompletionItemKind.Keyword;
}

function deactivate() {}

module.exports = { activate, deactivate };
