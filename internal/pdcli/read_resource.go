package pdcli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// A computed resource has no ORM contract to start the ordinary merge walk.
// Seed only its generated header; the generated read methods are merged after
// entity List/Watch additions and before the application's message overlay.
// A file may therefore contain both kinds without inventing an ORM entity or
// confusing generated methods with an app redeclaring a protected RPC.
func (r run) seedReadResourceContracts() error {
	err := filepath.WalkDir(r.pd, func(source string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(source, "_svc.resource.proto") {
			return err
		}
		rel, err := filepath.Rel(r.pd, source)
		if err != nil {
			return err
		}
		target := filepath.Join(r.svc, strings.TrimSuffix(rel, "_svc.resource.proto")+"_svc.g.proto")
		if _, err := os.Stat(target); err == nil {
			return nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		index := strings.Index(string(data), "\nservice ")
		if index < 0 {
			return fmt.Errorf("%s: generated read resource has no service", rel)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		return os.WriteFile(target, data[:index], 0644)
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func checkReadResourceRpcs(data []byte, file string) error {
	for _, method := range rpcName.FindAllSubmatch(data, -1) {
		switch string(method[1]) {
		case "Get", "List", "Watch":
		default:
			return fmt.Errorf("%s: a generated read resource cannot declare rpc %s", file, method[1])
		}
	}
	return nil
}
