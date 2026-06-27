package main

import (
	"regexp"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
)

// i 옵션을 사용해 조회하는 필터
func InsensitiveFilter(prefix string) bson.M {
	return bson.M{"tag": bson.M{
		"$regex":   "^" + regexp.QuoteMeta(prefix),
		"$options": "i",
	}}
}

// 대문자로 정규화해 조회하는 필터
func SensitiveFilter(prefix string) bson.M {
	upper := regexp.QuoteMeta(strings.ToUpper(prefix))
	return bson.M{"tag": bson.M{"$regex": "^" + upper}}
}
