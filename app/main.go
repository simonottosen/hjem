package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/tpanum/hjem"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// gormConfig keeps GORM's own log from drowning out the lookup lines. Its
// default logger reported every address-cache miss as an error — a miss is
// how the cache works, not a fault — and on a slow statement printed it with
// every bound value inlined, which for a batched address insert was fifty
// rows of street names and coordinates on a single line. Slow statements are
// still logged, as their SQL with placeholders. The plain writer replaces the
// default's colour codes and leading blank line, which reach `docker logs` as
// escape sequences and empty entries.
func gormConfig() *gorm.Config {
	return &gorm.Config{
		Logger: logger.New(log.New(os.Stdout, "", log.LstdFlags), logger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  logger.Warn,
			IgnoreRecordNotFoundError: true,
			ParameterizedQueries:      true,
		}),
	}
}

func main() {
	dbFile := flag.String("db-file", "hjem.db", "file for the database. default: hjem.db.")
	port := flag.Int("port", 8080, "port to use for the webserver. default: 8080")
	flag.Parse()

	log.Println("Starting hjem...")
	requireAddressConfig()
	db, dbName := connectDB(*dbFile)

	s := hjem.NewServer(db)

	// Log database status after AutoMigrate has run
	logDBStatus(db, dbName)

	fmt.Printf("Server started on http://localhost:%d\n", *port)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), s.Routes()); err != nil {
		fmt.Println("Error starting server:", err)
	}
}

// requireAddressConfig fails fast on missing address-service configuration.
// Since the DAWA cutover, every nearby-address lookup goes to Datafordeleren,
// which requires an API key. Without one the server still boots but each lookup
// dies mid-request with an opaque error, so refuse to start instead.
func requireAddressConfig() {
	if os.Getenv("DATAFORDELER_API_KEY") == "" {
		log.Println("FATAL: DATAFORDELER_API_KEY is not set.")
		log.Println("  Nearby-address search uses Datafordeleren (DAR GraphQL), which requires an API key.")
		log.Println("  Register at https://datafordeler.dk and export DATAFORDELER_API_KEY=<key>.")
		os.Exit(1)
	}
}

func logDBStatus(db *gorm.DB, dbName string) {
	var addrCount, saleCount, cacheCount int64
	db.Model(&hjem.Address{}).Count(&addrCount)
	db.Model(&hjem.Sale{}).Count(&saleCount)
	db.Model(&hjem.DawaQueryCache{}).Count(&cacheCount)

	log.Printf("Database: %s", dbName)
	log.Printf("  Addresses: %d", addrCount)
	log.Printf("  Sales:     %d", saleCount)
	log.Printf("  Cached queries: %d", cacheCount)
}

func connectDB(sqliteFile string) (*gorm.DB, string) {
	// Try PostgreSQL if POSTGRES_PASSWORD is set
	if pgPass := os.Getenv("POSTGRES_PASSWORD"); pgPass != "" {
		pgHost := envOrDefault("POSTGRES_HOST", "localhost")
		pgPort := envOrDefault("POSTGRES_PORT", "8777")
		pgUser := envOrDefault("POSTGRES_USER", "postgres")
		pgDB := envOrDefault("POSTGRES_DB", "hjem")

		dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable connect_timeout=5",
			pgHost, pgPort, pgUser, pgPass, pgDB)

		name := fmt.Sprintf("PostgreSQL %s:%s/%s", pgHost, pgPort, pgDB)
		log.Printf("Attempting %s connection...", name)

		// No falling back to SQLite. Setting POSTGRES_PASSWORD chose
		// PostgreSQL, and a server that quietly ran on SQLite instead would
		// start from an empty cache, write to a file in the container that the
		// next restart throws away, and report healthy throughout. Once the
		// few attempts below are spent, exiting leaves any further retrying to
		// the restart policy, where it is visible.
		db, err := connectPostgres(dsn)
		if err != nil {
			log.Fatalf("%s: %v (POSTGRES_PASSWORD is set, so not falling back to SQLite)", name, err)
		}
		return db, name
	}

	db, err := gorm.Open(sqlite.Open(sqliteFile), gormConfig())
	if err != nil {
		panic("failed to connect to any database")
	}

	sqlDB, err := db.DB()
	if err != nil {
		panic("failed to get underlying sql.DB")
	}
	sqlDB.SetMaxOpenConns(1)

	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA busy_timeout=5000")

	return db, fmt.Sprintf("SQLite %s", sqliteFile)
}

// A few attempts rather than one: a server started alongside its database
// (`docker compose up`) routinely comes up first, and without a restart policy
// a single refused connection would end it for good.
const (
	pgConnectAttempts = 5
	pgRetryDelay      = 3 * time.Second
)

func connectPostgres(dsn string) (*gorm.DB, error) {
	for attempt := 1; ; attempt++ {
		db, err := openPostgres(dsn)
		if err == nil {
			return db, nil
		}
		if attempt == pgConnectAttempts {
			return nil, err
		}
		log.Printf("PostgreSQL attempt %d/%d failed: %v — retrying in %s", attempt, pgConnectAttempts, err, pgRetryDelay)
		time.Sleep(pgRetryDelay)
	}
}

func openPostgres(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(dsn), gormConfig())
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(5)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
