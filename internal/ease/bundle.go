package ease

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const bundleMagic = "EASE_BUNDLE_V1"

// ReadEmbeddedSource checks whether `exePath` (or the current executable if empty)
// carries an appended Ease source bundle and returns it.
func ReadEmbeddedSource(exePath string) (string, bool, error) {
	if exePath == "" {
		var err error
		exePath, err = os.Executable()
		if err != nil {
			return "", false, err
		}
	}
	data, err := os.ReadFile(exePath)
	if err != nil {
		return "", false, err
	}
	src, ok := extractBundleBytes(data)
	return string(src), ok, nil
}

func extractBundleBytes(data []byte) ([]byte, bool) {
	trailerSize := 8 + len(bundleMagic)
	if len(data) < trailerSize {
		return nil, false
	}
	magicStart := len(data) - len(bundleMagic)
	if string(data[magicStart:]) != bundleMagic {
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
	trailerSize := 8 + len(bundleMagic)
	if len(data) < trailerSize {
		return data
	}
	magicStart := len(data) - len(bundleMagic)
	if string(data[magicStart:]) != bundleMagic {
		return data
	}
	lenStart := magicStart - 8
	srcLen := binary.LittleEndian.Uint64(data[lenStart:magicStart])
	if srcLen > uint64(lenStart) {
		return data
	}
	return data[:lenStart-int(srcLen)]
}

// BuildStandaloneExe compiles `source` into a self-contained native executable
// at `outExePath` using `baseExePath` (typically the running `ease` binary) as the runtime host.
func BuildStandaloneExe(baseExePath, source, outExePath string) error {
	// Validate syntax eagerly before emitting an executable.
	if _, err := ParseProgram(source); err != nil {
		return err
	}
	if baseExePath == "" {
		var err error
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

	srcBytes := []byte(source)
	var lenBuf [8]byte
	binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(srcBytes)))

	out := make([]byte, 0, len(baseExe)+len(srcBytes)+len(lenBuf)+len(bundleMagic))
	out = append(out, baseExe...)
	out = append(out, srcBytes...)
	out = append(out, lenBuf[:]...)
	out = append(out, bundleMagic...)

	if err := os.WriteFile(outExePath, out, 0o755); err != nil {
		return fmt.Errorf("write standalone exe %q: %w", outExePath, err)
	}
	return nil
}
