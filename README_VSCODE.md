# Ease in VS Code

The extension under [editors/vscode/](editors/vscode/) integrates VS Code with Ease's built-in Language Server (`ease lsp`):

- **Syntax highlighting** for `.ease` files (keywords, types, `#` comments, numbers, operators, and `{expr}` string interpolation)
- **Live diagnostics** while typing (errors, source spans, and hints directly from the Ease parser/typechecker)
- **Run & Test** directly in the editor (`Ctrl+F5`, ▶ Run button, or `Ease: Run Tests`)
- **Outline & Breadcrumbs** for `def` records, unions, `to` functions, and `check`/`test` blocks
- **Autocomplete** for Ease keywords plus functions and types in the current file
- **Editor helpers**: `#` comment toggling (`Ctrl+/`), auto-closing quotes/parens, and `to`/`def`/`if`/`repeat`/`for` → `end` indentation

## Setup

Both the `ease` CLI binary and the prebuilt VS Code extension (`ease-lang-<version>.vsix`) are attached to each **GitHub Release** (packaged by CI via `go run ./editors/vscode`).

1. **Install the extension (`ease-lang-<version>.vsix`)**:
   - **VS Code UI**: `Ctrl+Shift+X` → **`...`** → **`Install from VSIX...`**
   - **CLI**:
     ```bash
     code --install-extension ease-lang-<version>.vsix
     ```
2. **Provide the `ease` binary**:
   Download `ease` (`ease.exe` on Windows) from GitHub Releases and place it in your `PATH` (or workspace root), or build it locally:
   ```bash
   go build -o ease ./cmd/ease
   ```
   *(Windows: `go build -o ease.exe ./cmd/ease`)*

   You can also point VS Code directly to the binary in `settings.json`:
   ```json
   {
     "ease.executablePath": "path/to/ease"
   }
   ```

*(For extension development: open [editors/vscode/](editors/vscode/) in VS Code and press **F5** to launch an Extension Development Host—no `npm install` required.)*

## Architecture

- **VS Code Client** ([editors/vscode/extension.js](editors/vscode/extension.js)): Spawns `ease lsp` and communicates over JSON-RPC 2.0 (`stdin`/`stdout`).
- **Language Server** ([internal/ease/lsp.go](internal/ease/lsp.go), invoked via `ease lsp` in [cmd/ease/main.go](cmd/ease/main.go)): Runs `ParseProgram` from `internal/ease/` and publishes `EaseError` diagnostics.
- **Grammar & Config**: [editors/vscode/syntaxes/ease.tmLanguage.json](editors/vscode/syntaxes/ease.tmLanguage.json) and [editors/vscode/language-configuration.json](editors/vscode/language-configuration.json).
