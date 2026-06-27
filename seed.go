package main

import (
	"context"
	"crypto/rand"
	"math/big"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

func CreateTagDocuments(coll *mongo.Collection, count int) error {
	seen := make(map[string]struct{}, count)
	docs := make([]interface{}, 0, count)

	for len(docs) < count {
		tag, err := GenerateRandString(TAG_LEN)
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

	if _, err := coll.InsertMany(context.Background(), docs); err != nil {
		return err
	}

	return nil
}

// 당밤공에서 사용한 태그 생성 로직
func GenerateRandString(length int) (string, error) {
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
