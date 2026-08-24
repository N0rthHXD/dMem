package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/daemon"
	"github.com/N0rthHXD/dMem/internal/instance"
	"github.com/N0rthHXD/dMem/internal/maintenance"
	"github.com/N0rthHXD/dMem/internal/storage"
)

var version = "dev"

var (
	errUsage          = errors.New("usage error")
	errCommandFailed  = errors.New("maintenance command failed")
	errDatabaseExists = errors.New("database already exists")
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "backup", "restore", "init":
			if err := runMaintenance(ctx, os.Args[1], os.Args[2:], os.Stdout); err != nil {
				fmt.Fprintln(os.Stderr, maintenanceError(err))
				if errors.Is(err, errUsage) {
					os.Exit(2)
				}
				os.Exit(1)
			}
			return
		case "daemon":
			runDaemon(ctx, os.Args[2:])
			return
		}
	}
	runDaemon(ctx, os.Args[1:])
}

func runDaemon(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("dmemd", flag.ContinueOnError)
	cfg, err := config.Parse(fs, args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	d, err := daemon.Start(ctx, cfg)
	if err != nil {
		slog.Error("daemon startup failed", "error", err)
		os.Exit(1)
	}
	slog.Info("daemon ready")

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGracePeriod)
	defer cancel()
	if err := d.Close(shutdownCtx); err != nil {
		slog.Error("daemon shutdown failed", "error", err)
		os.Exit(1)
	}
}

type localConfig struct {
	dataDir      string
	databasePath string
	minimumFree  uint64
	timeout      time.Duration
}

func parseLocalConfig(fs *flag.FlagSet, args []string) (localConfig, error) {
	defaults, err := config.Default()
	if err != nil {
		return localConfig{}, errCommandFailed
	}
	dataDefault := defaults.DataDir
	if value := os.Getenv("DMEM_DATA_DIR"); value != "" {
		dataDefault = value
	}
	databaseDefault := os.Getenv("DMEM_DATABASE_PATH")
	dataDir := fs.String("data-dir", dataDefault, "persistent data directory")
	databasePath := fs.String("database-path", databaseDefault, "SQLite database path")
	minimumFree := fs.Uint64("minimum-free", defaults.DiskHardFree, "minimum free filesystem bytes (0 disables)")
	timeout := fs.Duration("timeout", 5*time.Minute, "maintenance operation timeout")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return localConfig{}, errUsage
	}
	absoluteData, err := filepath.Abs(*dataDir)
	if err != nil {
		return localConfig{}, errUsage
	}
	path := *databasePath
	if path == "" {
		path = filepath.Join(absoluteData, "dmem.db")
	}
	absoluteDatabase, err := filepath.Abs(path)
	if err != nil || filepath.Dir(absoluteDatabase) != absoluteData || *timeout <= 0 || *timeout > time.Hour {
		return localConfig{}, errUsage
	}
	return localConfig{dataDir: absoluteData, databasePath: absoluteDatabase, minimumFree: *minimumFree, timeout: *timeout}, nil
}

func runMaintenance(ctx context.Context, command string, args []string, output io.Writer) error {
	fs := flag.NewFlagSet("dmemd "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	switch command {
	case "backup":
		target := fs.String("target", "", "new backup directory")
		cfg, err := parseLocalConfig(fs, args)
		if err != nil || *target == "" {
			return errUsage
		}
		operationCtx, cancel := context.WithTimeout(ctx, cfg.timeout)
		defer cancel()
		if err := validateLocalDataDir(cfg.dataDir); err != nil {
			return err
		}
		if info, err := os.Lstat(cfg.databasePath); err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return maintenance.ErrBackupFailed
		}
		store, err := storage.Open(operationCtx, cfg.databasePath, storage.Options{})
		if err != nil {
			return maintenance.ErrBackupFailed
		}
		defer store.Close()
		manifest, err := maintenance.Backup(operationCtx, store, *target, maintenance.BackupOptions{DaemonVersion: version, MinimumFree: cfg.minimumFree})
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(struct {
			BackupID      string `json:"backup_id"`
			SchemaVersion int    `json:"schema_version"`
		}{manifest.BackupID, manifest.SchemaVersion})
	case "restore":
		source := fs.String("source", "", "backup directory")
		confirm := fs.Bool("confirm-replace", false, "confirm replacement of the data directory")
		cfg, err := parseLocalConfig(fs, args)
		if err != nil || *source == "" || !*confirm {
			return errUsage
		}
		operationCtx, cancel := context.WithTimeout(ctx, cfg.timeout)
		defer cancel()
		result, err := maintenance.Restore(operationCtx, *source, cfg.dataDir, maintenance.RestoreOptions{DatabaseFilename: filepath.Base(cfg.databasePath), MinimumFree: cfg.minimumFree})
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(struct {
			BackupID       string `json:"backup_id"`
			NormalizedJobs int    `json:"normalized_jobs"`
			FTSRebuilt     bool   `json:"fts_rebuilt"`
		}{result.Manifest.BackupID, result.NormalizedJobs, result.FTSRebuilt})
	case "init":
		cfg, err := parseLocalConfig(fs, args)
		if err != nil {
			return errUsage
		}
		if err := prepareLocalDataDir(cfg.dataDir); err != nil {
			return err
		}
		if _, err := os.Lstat(cfg.databasePath); err == nil {
			return errDatabaseExists
		} else if !errors.Is(err, os.ErrNotExist) {
			return errCommandFailed
		}
		lock, err := instance.Acquire(filepath.Join(cfg.dataDir, "daemon.lock"))
		if err != nil {
			if errors.Is(err, instance.ErrAlreadyRunning) {
				return maintenance.ErrOfflineRequired
			}
			return errCommandFailed
		}
		defer lock.Close()
		operationCtx, cancel := context.WithTimeout(ctx, cfg.timeout)
		defer cancel()
		store, err := storage.Open(operationCtx, cfg.databasePath, storage.Options{})
		if err != nil {
			return errCommandFailed
		}
		if err := store.Close(); err != nil {
			return errCommandFailed
		}
		return json.NewEncoder(output).Encode(struct {
			SchemaVersion int `json:"schema_version"`
		}{storage.SchemaVersion})
	default:
		return errUsage
	}
}

func prepareLocalDataDir(path string) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return maintenance.ErrUnsafePath
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return maintenance.ErrUnsafePath
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	if err := validateLocalDataDir(path); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func validateLocalDataDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return maintenance.ErrUnsafePath
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return maintenance.ErrUnsafePath
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(path) {
		return maintenance.ErrUnsafePath
	}
	return nil
}

func maintenanceError(err error) string {
	switch {
	case errors.Is(err, errUsage):
		return "maintenance: invalid_arguments"
	case errors.Is(err, errDatabaseExists):
		return "maintenance: database_exists"
	case errors.Is(err, maintenance.ErrOfflineRequired):
		return "maintenance: offline_required"
	case errors.Is(err, maintenance.ErrUnsafePath):
		return "maintenance: unsafe_path"
	case errors.Is(err, maintenance.ErrInvalidBackup):
		return "maintenance: invalid_backup"
	case errors.Is(err, maintenance.ErrIncompatibleBackup):
		return "maintenance: incompatible_backup"
	case errors.Is(err, maintenance.ErrInsufficientStorage):
		return "maintenance: insufficient_storage"
	case errors.Is(err, maintenance.ErrBackupFailed):
		return "maintenance: backup_failed"
	case errors.Is(err, maintenance.ErrRestoreFailed):
		return "maintenance: restore_failed"
	default:
		return "maintenance: operation_failed"
	}
}
