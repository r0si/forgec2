// Command mint-reg-secret prints the SECRET_ID / SECRET_B64 pair of a fresh v3
// per-implant registration secret, inserting the sealed row straight into a
// server's reg_secrets table.
//
// The Generate UI is the normal path; this tool exists for automation and lab
// setups with no operator session (the C-implant E2E flow in
// proto/c-implant/README.md is one). It reuses the server's own crypto
// (internal/crypto.RegSecretStore) and row model (internal/db.RegSecret), so
// what it writes is byte-for-byte what the UI would have written - and the
// running server resolves the row on the next registration, no restart needed.
//
//	go run ./cmd/mint-reg-secret -dsn "host=..." -config config.yaml
//	go run ./cmd/mint-reg-secret -dsn "host=..." -beacon-key <hex>
//
// -tenant <id> records the minting tenant so the first agent that registers
// with the secret lands in that team instead of the bootstrap default tenant
// (0 / omitted = legacy unscoped, same ownership behavior as before).
//
// Two operator-facing consequences are worth stating: the secret binds to the
// first agent id that registers with it, and the row is written directly to
// disk (no UI, no audit entry). Treat the printed secret like the API token it
// is - feed it to a build, not to a shared log.
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
	"gopkg.in/yaml.v3"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	dsn := flag.String("dsn", "host=localhost user=forgec2 password=forgec2 dbname=forgec2 port=5432 sslmode=disable", "PostgreSQL connection string")
	configPath := flag.String("config", "", "server config.yaml to read server.beacon_key from (ignored when -beacon-key is given)")
	keyHex := flag.String("beacon-key", "", "server.beacon_key as hex")
	tenantFlag := flag.String("tenant", "", "minting tenant id (numeric); omitted = legacy unscoped row")
	flag.Parse()

	if *tenantFlag != "" {
		if _, err := strconv.ParseUint(*tenantFlag, 10, 64); err != nil {
			fatal(fmt.Errorf("-tenant must be a numeric tenant id, got %q", *tenantFlag))
		}
	}

	key := *keyHex
	if key == "" && *configPath != "" {
		var err error
		if key, err = beaconKeyFromConfig(*configPath); err != nil {
			fatal(err)
		}
	}
	if key == "" {
		fatal(fmt.Errorf("beacon key required: pass -beacon-key <hex> or -config <path>"))
	}
	// config validation keeps beacon keys at 32 bytes (64 hex chars) and
	// regenerates weak ones, so anything shorter is a wrong key rather than a
	// deliberate choice: sealing under it would mint secrets the server can
	// never unseal.
	master, err := hex.DecodeString(key)
	if err != nil || len(master) < 32 {
		fatal(fmt.Errorf("beacon key must be the server's 64-char hex key (32 bytes): %v", err))
	}

	gdb, err := gorm.Open(postgres.Open(*dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		fatal(fmt.Errorf("open PostgreSQL: %w (start the server once so it creates the database)", err))
	}

	store := crypto.NewRegSecretStore(master)
	secret, err := store.GenerateSecret()
	if err != nil {
		fatal(err)
	}
	sealed, err := store.Seal(secret)
	if err != nil {
		fatal(err)
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		fatal(err)
	}
	row := db.RegSecret{ID: hex.EncodeToString(idBytes), SecretEnc: sealed, CreatedAt: time.Now()}
	if *tenantFlag != "" {
		tid, _ := strconv.ParseUint(*tenantFlag, 10, 64)
		row.TenantID = uint(tid)
	}
	if err := gdb.Create(&row).Error; err != nil {
		fatal(fmt.Errorf("insert reg_secrets row: %w", err))
	}

	fmt.Println("set SECRET_ID=" + row.ID)
	fmt.Println("set SECRET_B64=" + base64.StdEncoding.EncodeToString(secret))
	if row.TenantID > 0 {
		fmt.Fprintf(os.Stderr,
			"minted v3 registration secret %s (tenant %d) into PostgreSQL (no UI, no audit row); it binds to the first agent id that registers with it\n",
			row.ID, row.TenantID)
	} else {
		fmt.Fprintf(os.Stderr,
			"minted v3 registration secret %s into PostgreSQL (no UI, no audit row); it binds to the first agent id that registers with it\n",
			row.ID)
	}
}

func beaconKeyFromConfig(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var cfg struct {
		Server struct {
			BeaconKey string `yaml:"beacon_key"`
		} `yaml:"server"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg.Server.BeaconKey, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "mint-reg-secret:", err)
	os.Exit(1)
}
