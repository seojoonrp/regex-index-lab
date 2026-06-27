package main

import (
	"encoding/csv"
	"log"
	"os"
)

const TAG_LEN = 10

const URI = "mongodb://localhost:27017"
const DB_NAME = "regex-index-lab"
const COLL_NAME = "tags"

const ResultsFile = "results.csv"

func main() {
	db, err := ConnectMongo()
	if err != nil {
		log.Fatal(err)
	}
	defer DisconnectMongo(db)

	f, err := os.Create(ResultsFile)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	if err := RunBenchmark(db, w); err != nil {
		log.Fatal(err)
	}

	log.Printf("successfully wrote results in %s.", ResultsFile)
}
