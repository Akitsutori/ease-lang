# Ease in VS Code (Syntax-Highlighting & Fehlererkennung)

Mit der Extension unter [editors/vscode/](editors/vscode/) und dem eingebauten Language Server (`ease lsp`) bekommst du in VS Code:

- **Syntax-Highlighting** für `.ease`-Dateien (Keywords, Typen, `#`-Kommentare, Zahlen, Operatoren sowie `{expr}`-Interpolation in Strings)
- **Live-Fehlererkennung** beim Tippen (rote Unterstreichungen & Problems-Panel inkl. `Hint`-Texten direkt aus dem Ease-Parser)
- **Outline / Breadcrumbs** für `def`-Records, `union`-Typen, `to`-Funktionen und `check`-Blöcke
- **Autocomplete** für Ease-Keywords sowie im Dokument definierte Funktionen und Typen
- **Editor-Komfort**: `#`-Kommentar-Toggle (`Ctrl+/`), Klammer-/Anführungszeichen-Auto-Closing und Einrückung für `to` / `def` / `if` / `repeat` / `for` → `end`

## 1. Voraussetzungen

Du brauchst eine gebaute `ease`-Binary (`ease.exe` unter Windows bzw. `ease` unter Linux):

- Entweder aus den **GitHub Releases** herunterladen und ins `PATH` (oder direkt ins Projekt-Wurzelverzeichnis) legen,
- oder selbst bauen:
  ```bash
  go build -o ease ./cmd/ease
  ```
  *(Unter Windows: `go build -o ease.exe ./cmd/ease`)*

Die Extension sucht standardmäßig nach `ease` im `PATH` oder nach `ease.exe` / `ease` im geöffneten Workspace. Alternativ kannst du den Pfad in den VS-Code-Einstellungen festlegen:

```json
{
  "ease.executablePath": "D:/pfad/zu/ease.exe"
}
```

## 2. In VS Code einrichten

Es sind **keine** `npm install`- oder Build-Schritte nötig – `editors/vscode/extension.js` nutzt ausschließlich Node.js-Bordmittel.

### Option A: Direkt testen (Extension Development Host)
1. Ordner `editors/vscode` in VS Code öffnen (`File -> Open Folder...`).
2. **F5** drücken → es öffnet sich ein neues VS-Code-Fenster mit aktiver Ease-Extension.
3. Eine `.ease`-Datei (z. B. `examples/00_hello_world.ease`) öffnen.

### Option B: Über `.vsix` installieren (auch aus GitHub Releases)
Bei jedem Release (und CI-Lauf) wird `ease-lang-<version>.vsix` automatisch plattformunabhängig über `go run ./editors/vscode` ([build_vsix.go](editors/vscode/build_vsix.go)) gebaut und unter **GitHub Releases** bereitgestellt.

In VS Code installieren:
- Über die Oberfläche: `Ctrl+Shift+X` → **`...`** → **`Install from VSIX...`** → `.vsix`-Datei auswählen.
- Oder per CLI:
  ```powershell
  code --install-extension ease-lang-0.1.0.vsix
  ```

### Option C: Dauerhaft lokal verlinken
Kopiere oder verlinke `editors/vscode` in deinen VS-Code-Extensions-Ordner:

- **Windows (PowerShell):**
  ```powershell
  New-Item -ItemType Junction -Path "$env:USERPROFILE\.vscode\extensions\ease-lang" -Target "D:\DOWNLOADS\VSCode\ease-lang\editors\vscode"
  ```
- **Linux / macOS:**
  ```bash
  ln -s /pfad/zu/ease-lang/editors/vscode ~/.vscode/extensions/ease-lang
  ```

Danach VS Code neu laden (`Ctrl+Shift+P` → `Developer: Reload Window`).

## 3. Technische Architektur

- **VS-Code-Client** ([editors/vscode/extension.js](editors/vscode/extension.js)): Startet `ease lsp` als Kindprozess und spricht JSON-RPC 2.0 über `stdin`/`stdout`.
- **Language Server** ([internal/ease/lsp.go](internal/ease/lsp.go), CLI-Befehl `ease lsp` in [cmd/ease/main.go](cmd/ease/main.go)): Nutzt direkt `ParseProgram` aus `internal/ease/` und meldet `EaseError`-Positionen als `textDocument/publishDiagnostics`.
- **Grammatik & Editor-Regeln**: [editors/vscode/syntaxes/ease.tmLanguage.json](editors/vscode/syntaxes/ease.tmLanguage.json) und [editors/vscode/language-configuration.json](editors/vscode/language-configuration.json).
