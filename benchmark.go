package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	PrefixPoolSize = 50  // prefix pool 크기 (각 길이별)
	Concurrency    = 8   // worker 수
	ReqsPerWorker  = 125 // worker당 요청 수
	WarmupReqs     = 50  // 워밍업 요청 수
)

// 독립변수 N (태그 문서 수)
var Ns = []int{1000, 10000, 100000}

type Approach struct {
	Name   string
	Filter func(prefix string) bson.M
}

// 독립변수 Approach (조회 필터 방식)
var Approaches = []Approach{
	{Name: "i-regex", Filter: InsensitiveFilter},
	{Name: "sensitive", Filter: SensitiveFilter},
}

// prefix 구조체
type PrefixCase struct {
	Label     string
	PrefixLen int
	Prefixes  []string
}

// explain executionStats 결과값
type ExplainStats struct {
	KeysExamined int
	DocsExamined int
	NReturned    int
	Stages       string // "FETCH<-IXSCAN" / "COLLSCAN"
}

func RunBenchmark(db *mongo.Database, w *csv.Writer) error {
	ctx := context.Background()
	coll := db.Collection(COLL_NAME)

	if err := writeHeader(w); err != nil {
		return err
	}

	for _, n := range Ns {
		// reset
		log.Printf("[N=%d] reset", n)
		if err := reset(ctx, db, coll, n); err != nil {
			return fmt.Errorf("seed N=%d: %w", n, err)
		}

		// prefix 생성
		cases, err := buildPrefixCases(ctx, coll)
		if err != nil {
			return fmt.Errorf("prefix cases N=%d: %w", n, err)
		}

		warmup(ctx, coll, cases)

		for _, pc := range cases {
			// 대표 prefix 하나로 approach별 explain 결과 저장
			explainByApproach := map[string]ExplainStats{}
			for _, a := range Approaches {
				es, err := explainQuery(ctx, coll, a.Filter(pc.Prefixes[0]))
				if err != nil {
					return fmt.Errorf("explain N=%d %s/%s: %w", n, a.Name, pc.Label, err)
				}
				explainByApproach[a.Name] = es
				// explain 결과 출력
				log.Printf("[N=%d %s %s] keys=%d docs=%d nRet=%d stages=%s",
					n, pc.Label, a.Name, es.KeysExamined, es.DocsExamined, es.NReturned, es.Stages)
			}

			// 실제 부하
			byApproach := runConcurrent(ctx, coll, pc.Prefixes, Concurrency)
			// percentile 변환 후 csv 저장
			for _, a := range Approaches {
				p50, p95, p99 := percentiles(byApproach[a.Name])
				es := explainByApproach[a.Name]
				if err := writeRow(w, n, a.Name, pc.PrefixLen, p50, p95, p99, es); err != nil {
					return err
				}
			}
		}

		w.Flush()
	}

	return nil
}

// drop -> 인덱스 재생성 -> seed 생성
func reset(ctx context.Context, db *mongo.Database, coll *mongo.Collection, n int) error {
	if err := coll.Drop(ctx); err != nil {
		return err
	}
	if err := EnsureIndexes(db); err != nil {
		return err
	}
	return CreateTagDocuments(coll, n)
}

// prefix pool 생성
func buildPrefixCases(ctx context.Context, coll *mongo.Collection) ([]PrefixCase, error) {
	len2, err := randomPrefixes(2, PrefixPoolSize)
	if err != nil {
		return nil, err
	}
	len3, err := randomPrefixes(3, PrefixPoolSize)
	if err != nil {
		return nil, err
	}
	exact, err := sampleTags(ctx, coll, PrefixPoolSize)
	if err != nil {
		return nil, err
	}
	return []PrefixCase{
		{Label: "len2", PrefixLen: 2, Prefixes: len2},
		{Label: "len3", PrefixLen: 3, Prefixes: len3},
		{Label: "exact", PrefixLen: TAG_LEN, Prefixes: exact},
	}, nil
}

// 주어진 length의 랜덤 문자열 prefix n개 생성
func randomPrefixes(length, n int) ([]string, error) {
	out := make([]string, 0, n)
	for len(out) < n {
		p, err := GenerateRandString(length)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// 풀 태그 n개 뽑기 (exact case)
func sampleTags(ctx context.Context, coll *mongo.Collection, n int) ([]string, error) {
	// 태그 자체가 랜덤이라 natural order 앞 n개로 충분함
	cur, err := coll.Find(ctx, bson.M{}, options.Find().SetLimit(int64(n)))
	if err != nil {
		return nil, err
	}
	var docs []struct {
		Tag string `bson:"tag"`
	}
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.Tag)
	}
	return out, nil
}

// 각 prefix case에 대해 WarmupReqs 수만큼 요청
func warmup(ctx context.Context, coll *mongo.Collection, cases []PrefixCase) {
	for _, pc := range cases {
		for i := 0; i < WarmupReqs; i++ {
			a := Approaches[i%len(Approaches)]
			p := pc.Prefixes[i%len(pc.Prefixes)]
			_, _ = runQuery(ctx, coll, a.Filter(p)) // 결과/타이밍 버림
		}
	}
}

// 단일 필터 쿼리에 대한 시간 측정
func runQuery(ctx context.Context, coll *mongo.Collection, filter bson.M) (time.Duration, error) {
	start := time.Now()
	cur, err := coll.Find(ctx, filter)
	if err != nil {
		return 0, err
	}
	for cur.Next(ctx) { // decode 없이 배치만 소진
	}
	err = cur.Err()
	cur.Close(ctx)
	return time.Since(start), err
}

// 모든 prefix 검색 후 approach name - percentiles map으로 반환
func runConcurrent(ctx context.Context, coll *mongo.Collection, prefixes []string, concurrency int) map[string][]time.Duration {
	results := make([]map[string][]time.Duration, concurrency)
	var wg sync.WaitGroup

	for wID := range concurrency {
		wg.Add(1)
		go func(wID int) {
			defer wg.Done()

			local := map[string][]time.Duration{}
			for _, a := range Approaches {
				local[a.Name] = make([]time.Duration, 0, ReqsPerWorker)
			}

			// 동적 load balancing이 아니므로 static partitioning pattern
			for i := range ReqsPerWorker {
				a := Approaches[(wID+i)%len(Approaches)] // approach 선택 (매 요청마다 교대)
				p := prefixes[(wID*7+i)%len(prefixes)]   // prefix 선택, 7은 prefix pool 크기인 50과 서로소인 작은 수 (worker 분산)
				f := a.Filter(p)

				d, err := runQuery(ctx, coll, f)
				if err != nil {
					continue
				}

				local[a.Name] = append(local[a.Name], d)
			}
			results[wID] = local
		}(wID)
	}
	wg.Wait()

	// worker별 결과 합치기
	merged := map[string][]time.Duration{}
	for _, local := range results {
		for name, ds := range local {
			merged[name] = append(merged[name], ds...)
		}
	}
	return merged
}

// duration 슬라이스에서 percentile 계산
func percentiles(ds []time.Duration) (p50, p95, p99 time.Duration) {
	if len(ds) == 0 {
		return 0, 0, 0
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[pIdx(len(s), 0.50)], s[pIdx(len(s), 0.95)], s[pIdx(len(s), 0.99)]
}

func pIdx(n int, p float64) int {
	i := int(math.Ceil(p*float64(n))) - 1
	if i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}

// 해당 필터를 사용한 쿼리 explain
func explainQuery(ctx context.Context, coll *mongo.Collection, filter bson.M) (ExplainStats, error) {
	cmd := bson.D{
		{Key: "explain", Value: bson.D{
			{Key: "find", Value: coll.Name()},
			{Key: "filter", Value: filter},
		}},
		{Key: "verbosity", Value: "executionStats"},
	}

	var res struct {
		ExecutionStats struct {
			NReturned         int    `bson:"nReturned"`
			TotalKeysExamined int    `bson:"totalKeysExamined"`
			TotalDocsExamined int    `bson:"totalDocsExamined"`
			ExecutionStages   bson.M `bson:"executionStages"`
		} `bson:"executionStats"`
	}
	if err := coll.Database().RunCommand(ctx, cmd).Decode(&res); err != nil {
		return ExplainStats{}, err
	}

	stages := collectStages(res.ExecutionStats.ExecutionStages)
	return ExplainStats{
		KeysExamined: res.ExecutionStats.TotalKeysExamined,
		DocsExamined: res.ExecutionStats.TotalDocsExamined,
		NReturned:    res.ExecutionStats.NReturned,
		Stages:       joinStages(stages),
	}, nil
}

// 스테이지 트리를 root -> leaf로 훑어 stage 이름 수집(IXSCAN bounds / COLLSCAN)
func collectStages(m bson.M) []string {
	if m == nil {
		return nil
	}
	var out []string
	if s, ok := m["stage"].(string); ok {
		out = append(out, s)
	}
	if child, ok := m["inputStage"].(bson.M); ok {
		out = append(out, collectStages(child)...)
	}
	if children, ok := m["inputStages"].(bson.A); ok {
		for _, c := range children {
			if cm, ok := c.(bson.M); ok {
				out = append(out, collectStages(cm)...)
			}
		}
	}
	return out
}

func joinStages(stages []string) string {
	out := ""
	for i, s := range stages {
		if i > 0 {
			out += "<-"
		}
		out += s
	}
	return out
}

// csv
func writeHeader(w *csv.Writer) error {
	return w.Write([]string{
		"N", "approach", "prefix_len",
		"p50_ms", "p95_ms", "p99_ms",
		"keysExamined", "docsExamined", "nReturned",
	})
}

func writeRow(w *csv.Writer, n int, approach string, prefixLen int, p50, p95, p99 time.Duration, es ExplainStats) error {
	return w.Write([]string{
		strconv.Itoa(n),
		approach,
		strconv.Itoa(prefixLen),
		ms(p50), ms(p95), ms(p99),
		strconv.Itoa(es.KeysExamined),
		strconv.Itoa(es.DocsExamined),
		strconv.Itoa(es.NReturned),
	})
}

func ms(d time.Duration) string {
	return strconv.FormatFloat(float64(d.Microseconds())/1000.0, 'f', 3, 64)
}
