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
)

func main() {
	dbFile := flag.String("db-file", "hjem.db", "file for the database. default: hjem.db.")
	port := flag.Int("port", 8080, "port to use for the webserver. default: 8080")
	flag.Parse()

	log.Println("Starting hjem...")
	db := connectDB(*dbFile)

	s := hjem.NewServer(db)
	fmt.Printf("Server started on http://localhost:%d\n", *port)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), s.Routes()); err != nil {
		fmt.Println("Error starting server:", err)
	}
}

func connectDB(sqliteFile string) *gorm.DB {
	// Try PostgreSQL if POSTGRES_PASSWORD is set
	if pgPass := os.Getenv("POSTGRES_PASSWORD"); pgPass != "" {
		pgHost := envOrDefault("POSTGRES_HOST", "localhost")
		pgPort := envOrDefault("POSTGRES_PORT", "8777")
		pgUser := envOrDefault("POSTGRES_USER", "postgres")
		pgDB := envOrDefault("POSTGRES_DB", "hjem")

		dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable connect_timeout=5",
			pgHost, pgPort, pgUser, pgPass, pgDB)

		log.Printf("Attempting PostgreSQL connection to %s:%s/%s...", pgHost, pgPort, pgDB)

		db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
		if err == nil {
			sqlDB, err := db.DB()
			if err == nil {
				sqlDB.SetMaxOpenConns(25)
				sqlDB.SetMaxIdleConns(5)

				// Verify with a 5-second timeout
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := sqlDB.PingContext(ctx); err == nil {
					log.Printf("Connected to PostgreSQL at %s:%s/%s", pgHost, pgPort, pgDB)
					return db
				} else {
					log.Printf("PostgreSQL ping failed: %v — falling back to SQLite", err)
				}
			} else {
				log.Printf("PostgreSQL DB() failed: %v — falling back to SQLite", err)
			}
		} else {
			log.Printf("PostgreSQL connection failed: %v — falling back to SQLite", err)
		}
	}

	// Fallback to SQLite
	db, err := gorm.Open(sqlite.Open(sqliteFile), &gorm.Config{})
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

	log.Printf("Using SQLite at %s", sqliteFile)
	return db
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
