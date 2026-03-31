package main

import (
	"flag"
	"fmt"
	"net/http"

	"github.com/tpanum/hjem"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func main() {
	dbFile := flag.String("db-file", "hjem.db", "file for the database. default: hjem.db.")
	port := flag.Int("port", 8080, "port to use for the webserver. default: 8080")
	flag.Parse()

	db, err := gorm.Open(sqlite.Open(*dbFile), &gorm.Config{})
	if err != nil {
		panic("failed to connect database")
	}

	sqlDB, err := db.DB()
	if err != nil {
		panic("failed to get underlying sql.DB")
	}
	sqlDB.SetMaxOpenConns(1)

	// Enable WAL mode and busy timeout for concurrent access
	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA busy_timeout=5000")

	s := hjem.NewServer(db)
	fmt.Printf("Server started on http://localhost:%d\n", *port)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), s.Routes()); err != nil {
		fmt.Println("Error starting server:", err)
	}
}
