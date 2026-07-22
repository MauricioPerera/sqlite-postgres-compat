package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "github.com/tursodatabase/libsql-client-go/libsql"
)

func main() {
	db, err := sql.Open("libsql", os.Args[1])
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(1)
	}
	var out string
	if err := db.QueryRow("SELECT vector_extract(vector32('[1,2,3]'))").Scan(&out); err != nil {
		fmt.Println("query:", err)
		os.Exit(1)
	}
	fmt.Println("LIBSQL_OK", out)
}
