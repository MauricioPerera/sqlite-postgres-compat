package main

import (
	"encoding/json"
	"fmt"
	"os"

	"example.com/sqlite-postgres-compat/compat"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "uso: compat-audit <contract.json>")
		os.Exit(2)
	}

	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fatal(err)
	}
	var contract compat.Contract
	if err := json.Unmarshal(data, &contract); err != nil {
		fatal(err)
	}
	findings, err := compat.Audit(contract)
	if err != nil {
		fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(findings); err != nil {
		fatal(err)
	}
	if err := compat.RequireExact(findings); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
