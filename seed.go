package main

import (
	"context"
	"crypto/rand"
	"math/big"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

const TAG_LEN = 10

func CreateTagDocuments(coll *mongo.Collection, count int) error {
	seen := make(map[string]struct{}, count)
	docs := make([]interface{}, 0, count)

	for len(docs) < count {
		tag, err := generateTag(TAG_LEN)
		if err != nil {
			return err
		}

		// 중복이면 다시 뽑기
		if _, exists := seen[tag]; exists {
			continue
		}

		seen[tag] = struct{}{}
		docs = append(docs, bson.M{"tag": tag})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := coll.InsertMany(ctx, docs); err != nil {
		return err
	}

	return nil
}

// 당밤공에서 사용한 태그 생성 로직
func generateTag(length int) (string, error) {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, length)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			return "", err
		}
		b[i] = charset[n.Int64()]
	}
	return string(b), nil
}
