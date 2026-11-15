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

function resolveExecutable() {
  const configured = vscode.workspace
    .getConfiguration("ease")
    .get("executablePath", "ease");
  if (configured && configured !== "ease") {
    return configured;
  }
  const root = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (root) {
    const exeName = process.platform === "win32" ? "ease.exe" : "ease";
    const localBin = path.join(root, exeName);
    if (fs.existsSync(localBin)) {
      return localBin;
    }
  }
  return "ease";
}

async function runEaseSubcommand(subcommand, targetPath) {
  let filePath = targetPath;
  const activeEditor = vscode.window.activeTextEditor;
  if (!filePath && activeEditor && activeEditor.document.languageId === "ease") {
    if (activeEditor.document.isDirty) {
      await activeEditor.document.save();
    }
    filePath = activeEditor.document.uri.fsPath;
  }
  if (!filePath) {
    vscode.window.showWarningMessage("No active .ease file to run.");
    return;
  }

  const exe = resolveExecutable();
  const folder = vscode.workspace.getWorkspaceFolder(vscode.Uri.file(filePath));
  const exec = new vscode.ShellExecution(exe, [subcommand, filePath]);
  const task = new vscode.Task(
    { type: "ease", subcommand },
    folder || vscode.TaskScope.Workspace,
    `${subcommand} ${path.basename(filePath)}`,
    "ease",
    exec
  );
  task.presentationOptions = {
    reveal: vscode.TaskRevealKind.Always,
    panel: vscode.TaskPanelKind.Shared,
    clear: true,
    focus: true,
  };
  await vscode.tasks.executeTask(task);
}

class EaseDebugAdapter {
  constructor() {
    this._onDidSendMessage = new vscode.EventEmitter();
    this.onDidSendMessage = this._onDidSendMessage.event;
  }

  handleMessage(message) {
    if (message.type !== "request") return;
    const sendResponse = (body = {}) => {
      this._onDidSendMessage.fire({
        seq: 0,
        type: "response",
        request_seq: message.seq,
        success: true,
        command: message.command,
        body,
      });
    };

    switch (message.command) {
      case "initialize":
        sendResponse({});
        this._onDidSendMessage.fire({ seq: 0, type: "event", event: "initialized" });
        break;
      case "launch":
        sendResponse({});
        runEaseSubcommand("run", message.arguments && message.arguments.program).finally(() => {
          this._onDidSendMessage.fire({ seq: 0, type: "event", event: "terminated" });
        });
        break;
      default:
        sendResponse({});
        break;
    }
  }

  dispose() {
    this._onDidSendMessage.dispose();
  }
}

async function activate(context) {
  const output = vscode.window.createOutputChannel("Ease Language");
  const diagnostics = vscode.languages.createDiagnosticCollection("ease");
  const client = new EaseLspClient(resolveExecutable(), output, diagnostics);

  context.subscriptions.push(
    output,
    diagnostics,
    { dispose: () => client.stop() },
    vscode.commands.registerCommand("ease.runFile", () => runEaseSubcommand("run")),
    vscode.commands.registerCommand("ease.testFile", () => runEaseSubcommand("test")),
    vscode.debug.registerDebugAdapterDescriptorFactory("ease", {
      createDebugAdapterDescriptor() {
        return new vscode.DebugAdapterInlineImplementation(new EaseDebugAdapter());
      },
    }),
    vscode.debug.registerDebugConfigurationProvider("ease", {
      provideDebugConfigurations() {
        return [
          {
            type: "ease",
            request: "launch",
            name: "Run Ease File",
            program: "${file}",
          },
        ];
      },
      resolveDebugConfiguration(folder, config) {
        const active = vscode.window.activeTextEditor;
        const resolved = Object.assign(
          {
            type: "ease",
            request: "launch",
            name: "Run Ease File",
            program: "${file}",
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
    })
  );

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
    })
  );
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
