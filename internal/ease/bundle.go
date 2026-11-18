package ease

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	bundleMagic   = "EASE_BUNDLE_V1"
	bundleMagicV2 = "EASE_BUNDLE_V2"
)

// EmbeddedBundle describes a self-contained Ease program and any bundled file modules.
type EmbeddedBundle struct {
	Entry string            `json:"entry"`
	Files map[string]string `json:"files"`
}

// ReadEmbeddedSource checks whether `exePath` (or the current executable if empty)
// carries an appended Ease source bundle and returns the entry source.
func ReadEmbeddedSource(exePath string) (string, bool, error) {
	bundle, ok, err := ReadEmbeddedBundle(exePath)
	if err != nil || !ok {
		return "", ok, err
	}
	return bundle.Files[bundle.Entry], true, nil
}

// ReadEmbeddedBundle checks whether `exePath` (or the current executable if empty)
// carries a V1 single-file or V2 multi-file Ease bundle.
func ReadEmbeddedBundle(exePath string) (*EmbeddedBundle, bool, error) {
	if exePath == "" {
		var err error
		exePath, err = os.Executable()
		if err != nil {
			return nil, false, err
		}
	}
	data, err := os.ReadFile(exePath)
	if err != nil {
		return nil, false, err
	}
	if payload, ok := extractBundleBytes(data, bundleMagicV2); ok {
		var b EmbeddedBundle
		if err := json.Unmarshal(payload, &b); err != nil {
			return nil, false, fmt.Errorf("decode embedded bundle: %w", err)
		}
		if b.Entry == "" || len(b.Files) == 0 {
			return nil, false, errors.New("invalid embedded bundle: missing entry or files")
		}
		return &b, true, nil
	}
	if src, ok := extractBundleBytes(data, bundleMagic); ok {
		return &EmbeddedBundle{
			Entry: "main.ease",
			Files: map[string]string{"main.ease": string(src)},
		}, true, nil
	}
	return nil, false, nil
}

func extractBundleBytes(data []byte, magic string) ([]byte, bool) {
	trailerSize := 8 + len(magic)
	if len(data) < trailerSize {
		return nil, false
	}
	magicStart := len(data) - len(magic)
	if string(data[magicStart:]) != magic {
		return nil, false
	}
	lenStart := magicStart - 8
	srcLen := binary.LittleEndian.Uint64(data[lenStart:magicStart])
	if srcLen > uint64(lenStart) {
		return nil, false
	}
	srcStart := lenStart - int(srcLen)
	return data[srcStart:lenStart], true
}

func stripBundleBytes(data []byte) []byte {
	for _, magic := range []string{bundleMagicV2, bundleMagic} {
		trailerSize := 8 + len(magic)
		if len(data) < trailerSize {
			continue
		}
		magicStart := len(data) - len(magic)
		if string(data[magicStart:]) != magic {
			continue
		}
		lenStart := magicStart - 8
		srcLen := binary.LittleEndian.Uint64(data[lenStart:magicStart])
		if srcLen <= uint64(lenStart) {
			return data[:lenStart-int(srcLen)]
		}
	}
	return data
}

// BuildStandaloneExe compiles `source` into a self-contained native executable
// at `outExePath` using `baseExePath` (typically the running `ease` binary) as the runtime host.
func BuildStandaloneExe(baseExePath, source, outExePath string) error {
	return BuildStandaloneExeFromFile(baseExePath, source, "", outExePath)
}

// BuildStandaloneExeFromFile compiles `source` (resolved relative to `sourcePath`) and any
// transitive file modules into a self-contained native executable at `outExePath`.
func BuildStandaloneExeFromFile(baseExePath, source, sourcePath, outExePath string) error {
	var prog *Program
	var err error
	if sourcePath != "" {
		prog, err = ParseProgramFromFile(source, sourcePath)
	} else {
		prog, err = ParseProgram(source)
	}
	if err != nil {
		return err
	}

	if baseExePath == "" {
		baseExePath, err = os.Executable()
		if err != nil {
			return fmt.Errorf("locate current executable: %w", err)
		}
	}
	rawExe, err := os.ReadFile(baseExePath)
	if err != nil {
		return fmt.Errorf("read runtime binary %q: %w", baseExePath, err)
	}
	baseExe := stripBundleBytes(rawExe)
	if len(baseExe) == 0 {
		return errors.New("runtime binary is empty")
	}

	if err := os.MkdirAll(filepath.Dir(outExePath), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	var payload []byte
	magic := bundleMagic
	if hasFileImports(prog) {
		bundle, bErr := buildMultiFileBundle(source, sourcePath, prog)
		if bErr != nil {
			return bErr
		}
		payload, err = json.Marshal(bundle)
		if err != nil {
			return fmt.Errorf("encode embedded bundle: %w", err)
		}
		magic = bundleMagicV2
	} else {
		payload = []byte(source)
	}

	var lenBuf [8]byte
	binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(payload)))

	out := make([]byte, 0, len(baseExe)+len(payload)+len(lenBuf)+len(magic))
	out = append(out, baseExe...)
	out = append(out, payload...)
	out = append(out, lenBuf[:]...)
	out = append(out, magic...)

	if err := os.WriteFile(outExePath, out, 0o755); err != nil {
		return fmt.Errorf("write standalone exe %q: %w", outExePath, err)
	}
	return nil
}

func hasFileImports(prog *Program) bool {
	for _, imp := range prog.Imports {
		if imp.IsFileImport {
			return true
		}
	}
	return false
}

func buildMultiFileBundle(entrySource, entryPath string, entryProg *Program) (*EmbeddedBundle, error) {
	if entryPath == "" {
		entryPath = "main.ease"
	}
	absEntry, err := filepath.Abs(entryPath)
	if err != nil {
		return nil, fmt.Errorf("resolve entry path %q: %w", entryPath, err)
	}

	collected := map[string]string{
		absEntry: entrySource,
	}
	var walk func(curAbs string, p *Program) error
	walk = func(curAbs string, p *Program) error {
		curDir := filepath.Dir(curAbs)
		for _, imp := range p.Imports {
			if !imp.IsFileImport || imp.ModuleProg == nil {
				continue
			}
			modAbs, err := filepath.Abs(filepath.Clean(filepath.Join(curDir, imp.Path)))
			if err != nil {
				return fmt.Errorf("resolve imported module %q: %w", imp.Path, err)
			}
			if _, seen := collected[modAbs]; seen {
				continue
			}
			collected[modAbs] = imp.ModuleSource
			if err := walk(modAbs, imp.ModuleProg); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(absEntry, entryProg); err != nil {
		return nil, err
	}

	rootDir := commonDir(collected)
	entryRel, err := filepath.Rel(rootDir, absEntry)
	if err != nil {
		return nil, err
	}
	files := make(map[string]string, len(collected))
	for absPath, src := range collected {
		rel, err := filepath.Rel(rootDir, absPath)
		if err != nil {
			return nil, err
		}
		files[filepath.ToSlash(rel)] = src
	}
	return &EmbeddedBundle{
		Entry: filepath.ToSlash(entryRel),
		Files: files,
	}, nil
}

func commonDir(paths map[string]string) string {
	var common string
	for p := range paths {
		dir := filepath.Dir(p)
		if common == "" {
			common = dir
			continue
		}
		for !isSubOrEqualDir(common, dir) {
			parent := filepath.Dir(common)
			if parent == common {
				return common
			}
			common = parent
		}
	}
	return common
}

func isSubOrEqualDir(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}
