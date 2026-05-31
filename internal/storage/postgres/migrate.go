package postgres

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Migrate runs every .sql file in dir, in lexical order, against the pool.
// SQL must be idempotent — we use `IF NOT EXISTS` everywhere. There is no
// schema_migrations table by design: the POC's migrations are additive.
func (s *Store) Migrate(ctx context.Context, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("migrate: read dir %q: %w", dir, err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		files = append(files, e.Name())
	}
	sort.Strings(files)
	for _, name := range files {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("migrate: read %s: %w", name, err)
		}
		if _, err := s.Pool.Exec(ctx, string(raw)); err != nil {
			return fmt.Errorf("migrate: exec %s: %w", name, err)
		}
	}
	return nil
}
