package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
  <Default Extension=".vsixmanifest" ContentType="text/xml"/>
</Types>
`
	manifest := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<PackageManifest Version="2.0.0" xmlns="http://schemas.microsoft.com/developer/vsx-schema/2011">
  <Metadata>
    <Identity Language="en-US" Id="%s" Version="%s" Publisher="%s"/>
    <DisplayName>%s</DisplayName>
    <Description xml:space="preserve">%s</Description>
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
`, pkg.Name, version, pkg.Publisher, pkg.DisplayName, pkg.Description)

	if err := writeZipString(zw, "[Content_Types].xml", contentTypes); err != nil {
		fatalf("write [Content_Types].xml: %v", err)
	}
	if err := writeZipString(zw, "extension.vsixmanifest", manifest); err != nil {
		fatalf("write extension.vsixmanifest: %v", err)
	}
	if err := writeZipString(zw, "extension/package.json", string(pkgBytes)); err != nil {
		fatalf("pack package.json: %v", err)
	}

	files := []string{
		"extension.js",
		"language-configuration.json",
		filepath.Join("syntaxes", "ease.tmLanguage.json"),
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
	fmt.Printf("Created %s\n", outPath)
}

func writeZipString(zw *zip.Writer, name, content string) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, content)
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
