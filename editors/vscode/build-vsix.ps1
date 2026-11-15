$ErrorActionPreference = "Stop"

$extDir = $PSScriptRoot
$pkg = Get-Content (Join-Path $extDir "package.json") -Raw | ConvertFrom-Json
$version = $pkg.version
$stageDir = Join-Path $extDir ".vsix-stage"
$extStage = Join-Path $stageDir "extension"
$outVsix = Join-Path $extDir "ease-lang-$version.vsix"

if (Test-Path $stageDir) { Remove-Item $stageDir -Recurse -Force }
if (Test-Path $outVsix) { Remove-Item $outVsix -Force }

New-Item -ItemType Directory -Path (Join-Path $extStage "syntaxes") -Force | Out-Null

Copy-Item (Join-Path $extDir "package.json") $extStage
Copy-Item (Join-Path $extDir "extension.js") $extStage
Copy-Item (Join-Path $extDir "language-configuration.json") $extStage
Copy-Item (Join-Path $extDir "syntaxes\ease.tmLanguage.json") (Join-Path $extStage "syntaxes")

$contentTypes = @'
<?xml version="1.0" encoding="utf-8"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension=".json" ContentType="application/json"/>
  <Default Extension=".js" ContentType="application/javascript"/>
  <Default Extension=".md" ContentType="text/markdown"/>
  <Default Extension=".vsixmanifest" ContentType="text/xml"/>
</Types>
'@
[System.IO.File]::WriteAllText((Join-Path $stageDir "[Content_Types].xml"), $contentTypes, [System.Text.UTF8Encoding]::new($false))

$manifest = @"
<?xml version="1.0" encoding="utf-8"?>
<PackageManifest Version="2.0.0" xmlns="http://schemas.microsoft.com/developer/vsx-schema/2011">
  <Metadata>
    <Identity Language="en-US" Id="ease-lang" Version="$version" Publisher="ease-lang"/>
    <DisplayName>Ease Language</DisplayName>
    <Description xml:space="preserve">Syntax highlighting, live diagnostics, outline symbols, and autocomplete for the Ease programming language (.ease)</Description>
    <Categories>Programming Languages</Categories>
  </Metadata>
  <Installation>
    <InstallationTarget Id="Microsoft.VisualStudio.Code" Version="[1.80.0,)"/>
  </Installation>
  <Dependencies/>
  <Assets>
    <Asset Type="Microsoft.VisualStudio.Code.Manifest" Path="extension/package.json" Addressable="true"/>
  </Assets>
</PackageManifest>
"@
[System.IO.File]::WriteAllText((Join-Path $stageDir "extension.vsixmanifest"), $manifest, [System.Text.UTF8Encoding]::new($false))

Add-Type -AssemblyName System.IO.Compression.FileSystem
[System.IO.Compression.ZipFile]::CreateFromDirectory($stageDir, $outVsix)

Remove-Item $stageDir -Recurse -Force
Write-Output "Created: $outVsix"
