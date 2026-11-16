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
	if len(prog.Imports) == 0 {
		return nil
	}
	var mergedFuncs []*FuncDecl
	var mergedDefs []*DefDecl
	var mergedUnions []*UnionDecl
	var mergedBody []Stmt

	for _, imp := range prog.Imports {
		if seen[imp.Name] {
			continue
		}
		seen[imp.Name] = true
		modSrc, err := loadStdModuleSource(imp.Name)
		if err != nil {
			return newError(
				"ImportError",
				imp.Line,
				imp.Col,
				source,
				err.Error(),
				"Check available standard modules under std/, e.g. 'import network'.",
			)
		}
		modProg, err := parseProgramWithSeen(modSrc, seen)
		if err != nil {
			return err
		}
		for _, d := range modProg.Defs {
			d.Imported = true
		}
		for _, u := range modProg.Unions {
			u.Imported = true
		}
		mergedFuncs = append(mergedFuncs, modProg.Functions...)
		mergedDefs = append(mergedDefs, modProg.Defs...)
		mergedUnions = append(mergedUnions, modProg.Unions...)
		mergedBody = append(mergedBody, modProg.Body...)
	}

	prog.Functions = append(mergedFuncs, prog.Functions...)
	prog.Defs = append(mergedDefs, prog.Defs...)
	prog.Unions = append(mergedUnions, prog.Unions...)
	prog.Body = append(mergedBody, prog.Body...)
	return nil
}
