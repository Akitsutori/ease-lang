package ease

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed std/*.ease
var stdFS embed.FS

func loadStdModuleSource(name string) (string, error) {
	if stdEnv := strings.TrimSpace(os.Getenv("EASE_STD")); stdEnv != "" && stdEnv != "0" && !strings.EqualFold(stdEnv, "false") {
		stdDir := stdEnv
		if stdEnv == "1" || strings.EqualFold(stdEnv, "true") {
			stdDir = "std"
		}
		diskPath := filepath.Join(stdDir, name+".ease")
		if raw, err := os.ReadFile(diskPath); err == nil {
			fmt.Fprintf(os.Stderr, "[ease] note: using std/%s.ease override from %s (EASE_STD)\n", name, diskPath)
			return string(raw), nil
		}
	}
	raw, err := stdFS.ReadFile("std/" + name + ".ease")
	if err != nil {
		return "", fmt.Errorf("standard module %q not found", name)
	}
	return string(raw), nil
}

func resolveProgramImports(prog *Program, source string, seen map[string]bool) error {
	for _, imp := range prog.Imports {
		if imp.IsFileImport || imp.ModuleProg != nil {
			continue
		}
		if _, err := loadStdModuleSource(imp.Name); err != nil {
			return newError(
				"ImportError",
				imp.Line,
				imp.Col,
				source,
				err.Error(),
				"Check available standard modules under std/, e.g. 'import network'.",
			)
		}
	}
	return nil
}
