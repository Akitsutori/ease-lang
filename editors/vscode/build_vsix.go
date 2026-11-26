package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"ease-lang/internal/ease"
)

type vsCodePackage struct {
	Version     string `json:"version"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	Publisher   string `json:"publisher"`
	Name        string `json:"name"`
}

func main() {
	extDir := filepath.Join("editors", "vscode")
	if _, err := os.Stat(filepath.Join(extDir, "package.json")); err != nil {
		extDir = "."
	}

	pkgBytes, err := os.ReadFile(filepath.Join(extDir, "package.json"))
	if err != nil {
		fatalf("read package.json: %v", err)
	}
	var pkg vsCodePackage
	if err := json.Unmarshal(pkgBytes, &pkg); err != nil {
		fatalf("parse package.json: %v", err)
	}

	version := pkg.Version
	if envVer := strings.TrimPrefix(strings.TrimSpace(os.Getenv("EASE_VERSION")), "v"); envVer != "" {
		version = envVer
		var raw map[string]any
		if err := json.Unmarshal(pkgBytes, &raw); err != nil {
			fatalf("parse raw package.json: %v", err)
		}
		raw["version"] = version
		patched, err := json.MarshalIndent(raw, "", "  ")
		if err != nil {
			fatalf("marshal patched package.json: %v", err)
		}
		pkgBytes = append(patched, '\n')
	}

	iconPNG, err := ease.EncodeLogoPNG(128)
	if err != nil {
		fatalf("render icon.png: %v", err)
	}

	outPath := filepath.Join(extDir, fmt.Sprintf("%s-%s.vsix", pkg.Name, version))
	f, err := os.Create(outPath)
	if err != nil {
		fatalf("create %s: %v", outPath, err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	contentTypes := `<?xml version="1.0" encoding="utf-8"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension=".json" ContentType="application/json"/>
  <Default Extension=".js" ContentType="application/javascript"/>
  <Default Extension=".md" ContentType="text/markdown"/>
  <Default Extension=".png" ContentType="image/png"/>
  <Default Extension=".svg" ContentType="image/svg+xml"/>
  <Default Extension=".vsixmanifest" ContentType="text/xml"/>
</Types>
`
	manifest := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<PackageManifest Version="2.0.0" xmlns="http://schemas.microsoft.com/developer/vsx-schema/2011">
  <Metadata>
    <Identity Language="en-US" Id="%s" Version="%s" Publisher="%s"/>
    <DisplayName>%s</DisplayName>
    <Description xml:space="preserve">%s</Description>
    <Categories>Programming Languages,Debuggers</Categories>
    <Icon>extension/images/icon.png</Icon>
  </Metadata>
  <Installation>
    <InstallationTarget Id="Microsoft.VisualStudio.Code" Version="[1.80.0,)"/>
  </Installation>
  <Dependencies/>
  <Assets>
    <Asset Type="Microsoft.VisualStudio.Code.Manifest" Path="extension/package.json" Addressable="true"/>
    <Asset Type="Microsoft.VisualStudio.Services.Icons.Default" Path="extension/images/icon.png" Addressable="true"/>
  </Assets>
</PackageManifest>
`, pkg.Name, version, pkg.Publisher, pkg.DisplayName, pkg.Description)

	if err := writeZipBytes(zw, "[Content_Types].xml", []byte(contentTypes)); err != nil {
		fatalf("write [Content_Types].xml: %v", err)
	}
	if err := writeZipBytes(zw, "extension.vsixmanifest", []byte(manifest)); err != nil {
		fatalf("write extension.vsixmanifest: %v", err)
	}
	if err := writeZipBytes(zw, "extension/package.json", pkgBytes); err != nil {
		fatalf("pack package.json: %v", err)
	}
	if err := writeZipBytes(zw, "extension/images/icon.png", iconPNG); err != nil {
		fatalf("pack images/icon.png: %v", err)
	}

	files := []string{
		"extension.js",
		"language-configuration.json",
		filepath.Join("syntaxes", "ease.tmLanguage.json"),
		filepath.Join("images", "icon.svg"),
		filepath.Join("icons", "ease-icon-theme.json"),
	}
	for _, rel := range files {
		src := filepath.Join(extDir, rel)
		dst := filepath.ToSlash(filepath.Join("extension", rel))
		if err := copyFileToZip(zw, src, dst); err != nil {
			fatalf("pack %s: %v", rel, err)
		}
	}

	if err := zw.Close(); err != nil {
		fatalf("close zip: %v", err)
	}
	if err := verifyVSIX(outPath); err != nil {
		fatalf("verify %s: %v", outPath, err)
	}
	fmt.Printf("Created %s\n", outPath)
}

func verifyVSIX(vsixPath string) error {
	zr, err := zip.OpenReader(vsixPath)
	if err != nil {
		return err
	}
	defer zr.Close()

	present := make(map[string]uint64, len(zr.File))
	for _, f := range zr.File {
		present[f.Name] = f.UncompressedSize64
	}
	required := []string{
		"[Content_Types].xml",
		"extension.vsixmanifest",
		"extension/package.json",
		"extension/extension.js",
		"extension/language-configuration.json",
		"extension/syntaxes/ease.tmLanguage.json",
		"extension/images/icon.png",
		"extension/images/icon.svg",
		"extension/icons/ease-icon-theme.json",
	}
	for _, name := range required {
		sz, ok := present[name]
		if !ok {
			return fmt.Errorf("missing required archive entry %q", name)
		}
		if sz == 0 {
			return fmt.Errorf("archive entry %q is empty", name)
		}
	}
	return nil
}

func writeZipBytes(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func copyFileToZip(zw *zip.Writer, src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	w, err := zw.Create(dst)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, in)
	return err
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
